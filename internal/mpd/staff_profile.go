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
	"github.com/sendrec/sendrec/internal/validate"
)

const (
	// Watch pages inline the photo, so it must stay small. mpd-api sends 128 px.
	maxPhotoBytes = 64 << 10
	maxPhotoSide  = 512
	// A name longer than SendRec's user name limit, which sign-in applies too,
	// keeps the current name.
	maxNameRunes    = validate.MaxNameLength
	photoRetryAfter = 5 * time.Minute
	// photoDBTimeout bounds each database step of a background fetch. The read
	// itself is bounded by the handler's HTTP client, which times out at 5 s.
	photoDBTimeout = 10 * time.Second
	// defaultStaffName is what sign-in gives a staff member without a name.
	defaultStaffName = "MPD staff"
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
// version drops the whole profile, because a null one removes the photo. A
// null name keeps the current one: mpd-api sends no departure signal, so an
// offboarded staff member's name and photo are removed by the operator SQL in
// docs/mpd/README.md.
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
// the last one. Refresh validates inside its own transaction, which a failed write here
// would abort, so there the next request applies the profile.
//
// The photo the latest grant names is kept in the database (want_version), so
// every task agrees on it: a fetch stores its photo only while that is still
// the wanted version, and a removal clears the stored photo at once.
func (h *Handler) applyProfile(ctx context.Context, userID, token string, p *Profile) {
	if _, inTx := h.db.(pgx.Tx); p == nil || inTx {
		return
	}
	var name string
	var want, stored *string
	if err := h.db.QueryRow(ctx, `SELECT u.name,p.want_version,p.version FROM users u LEFT JOIN mpd_staff_photos p ON p.user_id=u.id WHERE u.id=$1`, userID).Scan(&name, &want, &stored); err != nil {
		slog.Warn("mpd profile not applied", "stage", "read", "error", err)
		return
	}
	if p.DisplayName != nil && *p.DisplayName != name {
		if _, err := h.db.Exec(ctx, `UPDATE users SET name=$2,updated_at=now() WHERE id=$1`, userID, *p.DisplayName); err != nil {
			slog.Warn("mpd profile not applied", "stage", "name", "error", err)
		}
	}
	if !sameVersion(want, p.PhotoVersion) || p.PhotoVersion == nil && stored != nil {
		if err := h.db.QueryRow(ctx, `INSERT INTO mpd_staff_photos(user_id,want_version) VALUES($1,$2)
			ON CONFLICT(user_id) DO UPDATE SET want_version=EXCLUDED.want_version,
			  version=CASE WHEN EXCLUDED.want_version IS NULL THEN NULL ELSE mpd_staff_photos.version END,
			  jpeg=CASE WHEN EXCLUDED.want_version IS NULL THEN NULL ELSE mpd_staff_photos.jpeg END,
			  updated_at=now()
			RETURNING version`, userID, p.PhotoVersion).Scan(&stored); err != nil {
			slog.Warn("mpd profile not applied", "stage", "photo_want", "error", err)
			return
		}
	}
	if p.PhotoVersion != nil && !sameVersion(stored, p.PhotoVersion) && h.photos != nil {
		h.photos.fetchLater(h, userID, token, *p.PhotoVersion)
	}
}

func sameVersion(a, b *string) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

// photoFetcher keeps photo fetches off the request path: one at a time per
// user in this process, and a version that failed waits photoRetryAfter
// before the next try, or is not tried again after mpd-api says it has no such
// photo (see finalPhotoStatus). The database holds
// the wanted version, so this only coordinates fetches within one task.
// Handler holds it by pointer because Refresh copies the handler.
type photoFetcher struct {
	mu     sync.Mutex
	busy   map[string]bool
	again  map[string]bool   // a grant asked for a photo while a fetch ran
	tokens map[string]string // the latest token that read a grant
	failed map[string]photoFailure
	wg     sync.WaitGroup
}

type photoFailure struct {
	version string
	at      time.Time
	final   bool // mpd-api will not give this version (finalPhotoStatus)
}

// waiting reports whether version failed and must not be read again yet.
func (p photoFailure) waiting(version string) bool {
	return p.version == version && (p.final || time.Since(p.at) < photoRetryAfter)
}

// finalPhotoStatus reports whether a status means mpd-api has no such photo,
// so reading the same version again cannot help. 401, 403, 408 and 429 can be
// token-specific or passing, so they wait photoRetryAfter like a 5xx.
func finalPhotoStatus(status int) bool {
	switch status {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusGone, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

func newPhotoFetcher() *photoFetcher {
	return &photoFetcher{busy: map[string]bool{}, again: map[string]bool{}, tokens: map[string]string{}, failed: map[string]photoFailure{}}
}

// photoStatusError is a photo read that mpd-api answered with a status other
// than 200.
type photoStatusError struct{ status int }

func (e *photoStatusError) Error() string { return fmt.Sprintf("photo status %d", e.status) }

// fetchLater reads version off the request path, unless this process already
// knows it failed and must wait, or a read for the user is running, in which
// case that read checks the wanted version again when it ends.
func (f *photoFetcher) fetchLater(h *Handler, userID, token, version string) {
	f.mu.Lock()
	if fail, ok := f.failed[userID]; ok && fail.waiting(version) {
		f.mu.Unlock()
		return
	}
	f.tokens[userID] = token
	if f.busy[userID] {
		f.again[userID] = true
		f.mu.Unlock()
		return
	}
	f.busy[userID] = true
	f.mu.Unlock()
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		f.run(h, userID)
	}()
}

// run fetches the wanted version until the stored photo matches it, the
// wanted version has failed, or the photo is no longer wanted. It reads the
// wanted version again after each attempt, so a version a grant names while
// a fetch runs is fetched next rather than dropped.
func (f *photoFetcher) run(h *Handler, userID string) {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), photoDBTimeout)
		var want, stored *string
		err := h.db.QueryRow(ctx, `SELECT want_version,version FROM mpd_staff_photos WHERE user_id=$1`, userID).Scan(&want, &stored)
		cancel()
		f.mu.Lock()
		again := f.again[userID]
		delete(f.again, userID)
		token := f.tokens[userID]
		fail, failed := f.failed[userID]
		done := err != nil || want == nil || sameVersion(want, stored) || failed && fail.waiting(*want)
		if done && !again {
			delete(f.busy, userID)
			delete(f.tokens, userID)
			f.mu.Unlock()
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				slog.Warn("mpd profile not applied", "stage", "photo_read", "error", err)
			}
			return
		}
		f.mu.Unlock()
		if done {
			continue // a grant arrived during the read: read the wanted version again
		}
		version := *want
		photo, err := h.fetchPhoto(context.Background(), token, version)
		if err == nil {
			ctx, cancel = context.WithTimeout(context.Background(), photoDBTimeout)
			_, err = h.db.Exec(ctx, `UPDATE mpd_staff_photos SET version=$2,jpeg=$3,updated_at=now() WHERE user_id=$1 AND want_version=$2`, userID, version, photo)
			cancel()
		}
		var status *photoStatusError
		final := errors.As(err, &status) && finalPhotoStatus(status.status)
		f.mu.Lock()
		if err != nil {
			f.failed[userID] = photoFailure{version: version, at: time.Now(), final: final}
		} else {
			delete(f.failed, userID)
		}
		f.mu.Unlock()
		switch {
		case final:
			warnProfile("mpd staff photo refused; not retried for this version", fmt.Sprintf("status %d", status.status))
		case err != nil:
			slog.Warn("mpd profile not applied", "stage", "photo", "error", err)
		}
	}
}

// fetchPhoto reads the staff member's photo from `{access URL}/photo` with the
// token that read the grant. It accepts only a small JPEG whose ETag names the
// version the grant gave, so a lagging read cannot store an old photo under a
// new version, and re-encodes it so no EXIF or other metadata segment reaches
// a patient's page. The EXIF orientation is applied to the pixels first, so the
// photo stays upright without the tag. The handler's HTTP client bounds the read at 5 s.
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
		return nil, &photoStatusError{status: resp.StatusCode}
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
	img, err := jpeg.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("photo is not a JPEG of at most 512 px")
	}
	var clean bytes.Buffer
	if err = jpeg.Encode(&clean, orient(img, jpegOrientation(body)), &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	if clean.Len() > maxPhotoBytes {
		return nil, errors.New("photo larger than 64 KiB after re-encoding")
	}
	return clean.Bytes(), nil
}
