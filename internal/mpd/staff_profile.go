package mpd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const (
	// Watch pages inline the photo, so it must stay small. mpd-api sends 128 px.
	maxPhotoBytes = 64 << 10
	maxPhotoSide  = 512
	// mpd-api allows 100-character first and last names, plus "Dr".
	maxNameRunes    = 256
	photoRetryAfter = 5 * time.Minute
	photoTimeout    = 15 * time.Second
)

var photoVersionPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// profileProblems holds the problems already logged. A grant is read about
// every 30 seconds per session, so each problem is logged once per process.
var profileProblems sync.Map

func warnProfile(message, reason string) {
	if _, seen := profileProblems.LoadOrStore(message+reason, true); !seen {
		slog.Warn(message, "reason", reason)
	}
}

// parseProfile reads the grant's profile and never fails the grant. A missing
// or bad name is dropped, which keeps the last one. A missing or bad photo
// version drops the whole profile, because a null one removes the photo.
func parseProfile(raw json.RawMessage) *Profile {
	if len(raw) == 0 {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		warnProfile("mpd profile ignored", "shape")
		return nil
	}
	if fields == nil {
		return nil
	}
	var version *string
	if v, ok := fields["photoVersion"]; !ok || json.Unmarshal(v, &version) != nil || version != nil && !photoVersionPattern.MatchString(*version) {
		warnProfile("mpd profile ignored", "photo_version")
		return nil
	}
	p := &Profile{PhotoVersion: version}
	if v, ok := fields["displayName"]; ok {
		var name *string
		if json.Unmarshal(v, &name) != nil {
			warnProfile("mpd profile name ignored", "type")
		} else if name != nil {
			trimmed := strings.TrimSpace(*name)
			if trimmed == "" || utf8.RuneCountInString(trimmed) > maxNameRunes || strings.IndexFunc(trimmed, unicode.IsControl) >= 0 {
				warnProfile("mpd profile name ignored", "value")
			} else {
				p.DisplayName = &trimmed
			}
		}
	}
	return p
}

// applyProfile copies the staff member's name and photo from a grant to their
// SendRec user, so watch pages show who recorded. Sign-in and each grant
// refresh call it. It writes only what changed, fetches a new photo off the
// request path, and never fails the request. A profile without a name keeps
// the last one. Refresh validates inside its own transaction, which a failed
// write here would abort, so there the next request applies the profile.
func (h *Handler) applyProfile(ctx context.Context, userID, token string, p *Profile) {
	if _, inTx := h.db.(pgx.Tx); p == nil || inTx {
		return
	}
	var name string
	var version *string
	if err := h.db.QueryRow(ctx, `SELECT u.name,p.version FROM users u LEFT JOIN mpd_staff_photos p ON p.user_id=u.id WHERE u.id=$1`, userID).Scan(&name, &version); err != nil {
		slog.Warn("mpd profile not applied", "stage", "read", "error", err)
		return
	}
	if p.DisplayName != nil && *p.DisplayName != name {
		if _, err := h.db.Exec(ctx, `UPDATE users SET name=$2,updated_at=now() WHERE id=$1`, userID, *p.DisplayName); err != nil {
			slog.Warn("mpd profile not applied", "stage", "name", "error", err)
		}
	}
	if p.PhotoVersion == nil {
		photoFetches.want.Delete(userID)
	} else {
		photoFetches.want.Store(userID, *p.PhotoVersion)
	}
	if sameVersion(version, p.PhotoVersion) {
		return
	}
	if p.PhotoVersion == nil {
		if _, err := h.db.Exec(ctx, `DELETE FROM mpd_staff_photos WHERE user_id=$1`, userID); err != nil {
			slog.Warn("mpd profile not applied", "stage", "photo_remove", "error", err)
		}
		return
	}
	h.fetchPhotoLater(userID, token, *p.PhotoVersion)
}

func sameVersion(a, b *string) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

// photoFetches keeps photo fetches off the request path: one at a time per
// user, and a version that failed waits photoRetryAfter before the next try.
// It lives outside Handler because Refresh copies the handler.
var photoFetches struct {
	busy   sync.Map // user ID → struct{}
	failed sync.Map // user ID → photoFailure
	want   sync.Map // user ID → the version the latest grant names; absent for none
	wg     sync.WaitGroup
}

type photoFailure struct {
	version string
	at      time.Time
}

func (h *Handler) fetchPhotoLater(userID, token, version string) {
	if f, ok := photoFetches.failed.Load(userID); ok && f.(photoFailure).version == version && time.Since(f.(photoFailure).at) < photoRetryAfter {
		return
	}
	if _, busy := photoFetches.busy.LoadOrStore(userID, struct{}{}); busy {
		return
	}
	photoFetches.wg.Add(1)
	go func() {
		defer photoFetches.wg.Done()
		defer photoFetches.busy.Delete(userID)
		ctx, cancel := context.WithTimeout(context.Background(), photoTimeout)
		defer cancel()
		photo, err := h.fetchPhoto(ctx, token, version)
		if err == nil {
			_, err = h.db.Exec(ctx, `INSERT INTO mpd_staff_photos(user_id,version,jpeg) VALUES($1,$2,$3) ON CONFLICT(user_id) DO UPDATE SET version=EXCLUDED.version,jpeg=EXCLUDED.jpeg,updated_at=now()`, userID, version, photo)
		}
		// A grant that changed or removed the photo during the read wins. If it
		// deleted the row before this store, this removes the store; if after,
		// its own delete does.
		if w, ok := photoFetches.want.Load(userID); err == nil && (!ok || w.(string) != version) {
			_, err = h.db.Exec(ctx, `DELETE FROM mpd_staff_photos WHERE user_id=$1 AND version=$2`, userID, version)
		}
		if err != nil {
			photoFetches.failed.Store(userID, photoFailure{version: version, at: time.Now()})
			slog.Warn("mpd profile not applied", "stage", "photo", "error", err)
			return
		}
		photoFetches.failed.Delete(userID)
	}()
}

// fetchPhoto reads the staff member's photo from `{access URL}/photo` with the
// token that read the grant. It accepts only a small JPEG whose ETag names the
// version the grant gave, so a lagging read cannot store an old photo under a
// new version.
func (h *Handler) fetchPhoto(ctx context.Context, token, version string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(h.cfg.AccessURL, "/")+"/photo", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("photo status %d", resp.StatusCode)
	}
	if resp.Header.Get("ETag") != `"`+version+`"` {
		return nil, errors.New("photo is not the version the grant names")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPhotoBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxPhotoBytes {
		return nil, errors.New("photo larger than 64 KiB")
	}
	size, err := jpeg.DecodeConfig(bytes.NewReader(body))
	if err != nil || size.Width > maxPhotoSide || size.Height > maxPhotoSide {
		return nil, errors.New("photo is not a JPEG of at most 512 px")
	}
	if _, err = jpeg.Decode(bytes.NewReader(body)); err != nil {
		return nil, errors.New("photo is not a JPEG of at most 512 px")
	}
	return body, nil
}
