package mpd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"unicode"
)

// Watch pages inline the photo, so it must stay small. mpd-api sends 128 px.
const (
	maxPhotoBytes = 64 << 10
	maxPhotoSide  = 512
	maxNameBytes  = 100
)

var photoVersionPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// clean trims the display name and drops a bad one, which keeps the last name.
// It reports whether the photo version is usable; a null one would remove the
// photo, so a bad one cannot stand in for it.
func (p *Profile) clean() bool {
	if p.DisplayName != nil {
		name := strings.TrimSpace(*p.DisplayName)
		p.DisplayName = &name
		if name == "" || len(name) > maxNameBytes || strings.IndexFunc(name, unicode.IsControl) >= 0 {
			p.DisplayName = nil
		}
	}
	return p.PhotoVersion == nil || photoVersionPattern.MatchString(*p.PhotoVersion)
}

// applyProfile copies the staff member's name and photo from a grant to their
// SendRec user, so watch pages show who recorded. Sign-in and each grant
// refresh call it. It writes only what changed and never fails the request. A
// profile without a name keeps the last one.
func (h *Handler) applyProfile(ctx context.Context, userID, token string, p *Profile) {
	if p == nil {
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
	if sameVersion(version, p.PhotoVersion) {
		return
	}
	if p.PhotoVersion == nil {
		if _, err := h.db.Exec(ctx, `DELETE FROM mpd_staff_photos WHERE user_id=$1`, userID); err != nil {
			slog.Warn("mpd profile not applied", "stage", "photo_remove", "error", err)
		}
		return
	}
	photo, err := h.fetchPhoto(ctx, token)
	if err != nil {
		slog.Warn("mpd profile not applied", "stage", "photo_fetch", "error", err)
		return
	}
	if _, err = h.db.Exec(ctx, `INSERT INTO mpd_staff_photos(user_id,version,jpeg) VALUES($1,$2,$3) ON CONFLICT(user_id) DO UPDATE SET version=EXCLUDED.version,jpeg=EXCLUDED.jpeg,updated_at=now()`, userID, *p.PhotoVersion, photo); err != nil {
		slog.Warn("mpd profile not applied", "stage", "photo_store", "error", err)
	}
}

func sameVersion(a, b *string) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

// fetchPhoto reads the staff member's photo from `{access URL}/photo` with the
// token that read the grant. It accepts only a small JPEG.
func (h *Handler) fetchPhoto(ctx context.Context, token string) ([]byte, error) {
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
