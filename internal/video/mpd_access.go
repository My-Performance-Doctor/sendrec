package video

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/sendrec/sendrec/internal/auth"
	"github.com/sendrec/sendrec/internal/database"
	"github.com/sendrec/sendrec/internal/mpd"
	objectstore "github.com/sendrec/sendrec/internal/storage"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/sendrec/sendrec/internal/httputil"
)

// MPDPublicAccess is installed on every registered token route. Persisted policy
// remains enforced when login/event delivery is disabled during rollback.
func (h *Handler) MPDPublicAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := chi.URLParam(r, "shareToken")
		if token == "" {
			httputil.WriteError(w, 404, "recording unavailable")
			return
		}
		managedTokens := map[string]bool{}
		if strings.Contains(r.URL.Path, "/playlist/") {
			rows, err := h.db.Query(r.Context(), `SELECT v.share_token,s.published,s.deleted_at,v.share_password,v.email_gate_enabled,v.share_expires_at FROM playlists p JOIN playlist_videos pv ON pv.playlist_id=p.id JOIN videos v ON v.id=pv.video_id JOIN mpd_video_state s ON s.video_id=v.id WHERE p.share_token=$1`, token)
			if err != nil {
				httputil.WriteError(w, 503, "access unavailable")
				return
			}
			defer rows.Close()
			for rows.Next() {
				var share string
				var published bool
				var deleted *time.Time
				var password *string
				var email bool
				var expires *time.Time
				if rows.Scan(&share, &published, &deleted, &password, &email, &expires) != nil {
					httputil.WriteError(w, 503, "access unavailable")
					return
				}
				managedTokens[share] = true
				if !published || deleted != nil || expires != nil && time.Now().After(*expires) {
					httputil.WriteError(w, 403, "a playlist recording requires individual access")
					return
				}
				if !h.enforceWatchAccess(w, r, share, password, email) {
					return
				}
			}
			if rows.Err() != nil {
				httputil.WriteError(w, 503, "access unavailable")
				return
			}
		} else {
			var published bool
			var deleted *time.Time
			var password *string
			var email bool
			var expires *time.Time
			err := h.db.QueryRow(r.Context(), `SELECT s.published,s.deleted_at,v.share_password,v.email_gate_enabled,v.share_expires_at FROM videos v JOIN mpd_video_state s ON s.video_id=v.id WHERE v.share_token=$1`, token).Scan(&published, &deleted, &password, &email, &expires)
			if err != nil && err != pgx.ErrNoRows {
				httputil.WriteError(w, 503, "access unavailable")
				return
			}
			if err == nil {
				managedTokens[token] = true
				w.Header().Set("Cache-Control", "private, no-store")
				if !published || deleted != nil || expires != nil && time.Now().After(*expires) {
					httputil.WriteError(w, 404, "recording unavailable")
					return
				}
				// HTML pages render the existing password form; verification sets its cookie.
				page := strings.HasPrefix(r.URL.Path, "/watch/") || strings.HasPrefix(r.URL.Path, "/embed/")
				verify := strings.HasSuffix(r.URL.Path, "/verify")
				identify := strings.HasSuffix(r.URL.Path, "/identify")
				if !page && !verify && !h.enforceWatchAccess(w, r, token, password, email && !identify) {
					return
				}
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), mpdManagedTokensKey{}, managedTokens)))
	})
}

type mpdManagedTokensKey struct{}
type mpdKnownObjectsKey struct{}

func managedShare(ctx context.Context, token string) bool {
	tokens, _ := ctx.Value(mpdManagedTokensKey{}).(map[string]bool)
	return tokens[token]
}

func knownMPDObject(ctx context.Context, key string, managed bool) context.Context {
	return context.WithValue(ctx, mpdKnownObjectsKey{}, map[string]bool{key: managed})
}

type boundedMPDStorage struct {
	ObjectStorage
	db database.DBTX
}

// Policy follows persisted recording membership, even when MPD flags are off.
// Explicit per-row state avoids another lookup when a list already loaded it.
func (s boundedMPDStorage) downloadExpiry(ctx context.Context, key string, expiry time.Duration) (time.Duration, error) {
	if expiry <= 5*time.Minute {
		return expiry, nil
	}
	known, _ := ctx.Value(mpdKnownObjectsKey{}).(map[string]bool)
	managed, ok := known[key]
	if !ok {
		err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM videos v JOIN mpd_video_state s ON s.video_id=v.id WHERE $1 IN (v.file_key,v.thumbnail_key,v.transcript_key,v.webcam_key))`, key).Scan(&managed)
		if err != nil {
			return 0, err
		}
	}
	if managed {
		return 5 * time.Minute, nil
	}
	return expiry, nil
}
func (s boundedMPDStorage) GenerateDownloadURL(ctx context.Context, key string, expiry time.Duration) (string, error) {
	expiry, err := s.downloadExpiry(ctx, key, expiry)
	if err != nil {
		return "", err
	}
	return s.ObjectStorage.GenerateDownloadURL(ctx, key, expiry)
}
func (s boundedMPDStorage) GenerateDownloadURLWithDisposition(ctx context.Context, key, filename string, expiry time.Duration) (string, error) {
	expiry, err := s.downloadExpiry(ctx, key, expiry)
	if err != nil {
		return "", err
	}
	return s.ObjectStorage.GenerateDownloadURLWithDisposition(ctx, key, filename, expiry)
}
func (h *Handler) BoundMPDMediaURLs() {
	h.storage = boundedMPDStorage{ObjectStorage: h.storage, db: h.db}
}

// RenewWatchMedia repeats publication middleware plus the existing password,
// expiry and email policy without recording a page view.
func (h *Handler) RenewWatchMedia(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "shareToken")
	var file string
	var password *string
	var transcript *string
	var version int
	var expires *time.Time
	var email bool
	err := h.db.QueryRow(r.Context(), `SELECT file_key,share_password,share_expires_at,email_gate_enabled,transcript_key,media_version FROM videos WHERE share_token=$1 AND status='ready'`, token).Scan(&file, &password, &expires, &email, &transcript, &version)
	if err != nil || expires != nil && time.Now().After(*expires) {
		httputil.WriteError(w, 404, "recording unavailable")
		return
	}
	if !h.enforceWatchAccess(w, r, token, password, email) {
		return
	}
	url, err := h.storage.GenerateDownloadURL(r.Context(), file, 5*time.Minute)
	if err != nil {
		httputil.WriteError(w, 503, "media unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	var captions string
	if transcript != nil {
		captions, _ = h.storage.GenerateDownloadURL(r.Context(), *transcript, 5*time.Minute)
	}
	httputil.WriteJSON(w, 200, map[string]any{"videoUrl": url, "transcriptUrl": captions, "mediaVersion": version, "expiresAt": time.Now().Add(5 * time.Minute)})
}

// RenewUpload signs only the existing owner's pending object. It cannot create
// another recording or overwrite a finalized one.
func (h *Handler) RenewUpload(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Kind        string `json:"kind"`
		ContentType string `json:"contentType"`
		FileSize    int64  `json:"fileSize"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&input) != nil || input.FileSize <= 0 || h.maxUploadBytes > 0 && input.FileSize > h.maxUploadBytes {
		httputil.WriteError(w, 400, "invalid upload")
		return
	}
	if input.ContentType != "video/webm" && input.ContentType != "video/mp4" && input.ContentType != "video/quicktime" {
		httputil.WriteError(w, 400, "unsupported upload type")
		return
	}
	var key, content string
	var webcam *string
	var size int64
	err := h.db.QueryRow(r.Context(), `SELECT file_key,webcam_key,content_type,file_size FROM videos WHERE id=$1 AND user_id=$2 AND status='uploading'`, chi.URLParam(r, "id"), auth.UserIDFromContext(r.Context())).Scan(&key, &webcam, &content, &size)
	if err != nil {
		httputil.WriteError(w, 409, "upload no longer pending")
		return
	}
	if input.Kind == "webcam" {
		if webcam == nil {
			httputil.WriteError(w, 400, "camera object unavailable")
			return
		}
		key = *webcam
	} else if input.Kind != "screen" || input.FileSize != size || input.ContentType != content {
		httputil.WriteError(w, 400, "upload changed")
		return
	}
	if _, managed := mpd.PrincipalFromContext(r.Context()); managed {
		actualSize, actualContent, headErr := h.storage.HeadObject(r.Context(), key)
		if headErr == nil {
			if actualSize != input.FileSize || actualContent != input.ContentType {
				httputil.WriteError(w, 409, "existing upload does not match recording")
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			httputil.WriteJSON(w, 200, map[string]any{"uploaded": true})
			return
		}
		if !errors.Is(headErr, objectstore.ErrObjectNotFound) {
			httputil.WriteError(w, 503, "upload verification unavailable")
			return
		}
	}
	signed, uploadHeaders, err := h.recordingUploadURL(r.Context(), key, input.ContentType, input.FileSize)
	if err != nil {
		httputil.WriteError(w, 503, "upload unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httputil.WriteJSON(w, 200, map[string]any{"uploadUrl": signed, "uploadHeaders": uploadHeaders, "uploaded": false})
}

// Playback proofs cannot bypass expiry or email checks on the watch route.
func (h *Handler) MPDPlaybackAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := chi.URLParam(r, "shareToken")
		var password *string
		var expires *time.Time
		var email bool
		err := h.db.QueryRow(r.Context(), `SELECT share_password,share_expires_at,email_gate_enabled FROM videos WHERE share_token=$1 AND status='ready'`, token).Scan(&password, &expires, &email)
		if err != nil || expires != nil && time.Now().After(*expires) {
			httputil.WriteError(w, 404, "recording unavailable")
			return
		}
		if !h.enforceWatchAccess(w, r, token, password, email) {
			return
		}
		next.ServeHTTP(w, r)
	})
}
