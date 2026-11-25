package video

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sendrec/sendrec/internal/database"
	"github.com/sendrec/sendrec/internal/httputil"
)

// MPDImportConfig is the operator import of MPD's Vimeo videos (SNDR-1). It has its own
// service tokens, separate from the MPD service API's, and is off unless both the tokens
// and the owner are configured.
type MPDImportConfig struct {
	// Hex SHA-256 verifiers of the import tokens, current and optionally previous.
	TokenHashes []string
	// The SendRec user that owns every imported video: an MPD staff identity in the
	// managed workspace, kept for the migration so imports never reach a clinician's list.
	OwnerUserID string
	WorkspaceID string
}

func (c MPDImportConfig) Enabled() bool {
	return len(c.TokenHashes) > 0 && c.OwnerUserID != "" && c.WorkspaceID != ""
}

// MPDImporter creates one managed video per Vimeo video from a file the migration's
// download job has already written to importKey in the recordings bucket. The video keeps
// its original recording date, goes through the usual encoder, and gets no transcript.
// It is never published here: mpd-api attaches it to its report and publishes it.
type MPDImporter struct {
	h       *Handler
	cfg     MPDImportConfig
	enqueue func(ctx context.Context, job JobType, videoID string, payload map[string]any)
	now     func() time.Time
}

func (h *Handler) MPDImporter(cfg MPDImportConfig) *MPDImporter {
	return &MPDImporter{h: h, cfg: cfg, enqueue: h.EnqueueJob, now: time.Now}
}

var vimeoID = regexp.MustCompile(`^[0-9]{1,20}$`)

// importKey is where the download job puts a Vimeo video's file.
func importKey(vimeo string) string { return "imports/vimeo/" + vimeo + ".mp4" }

// importTitle names an imported video without anything from Vimeo: Vimeo titles can
// identify patients, and mpd-api keeps the report's own title.
const importTitle = "MPD result video"

func (i *MPDImporter) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		sum := sha256.Sum256([]byte(token))
		matched := 0
		for _, hash := range i.cfg.TokenHashes {
			raw, e := hex.DecodeString(hash)
			if e == nil && len(raw) == 32 {
				matched |= subtle.ConstantTimeCompare(raw, sum[:])
			}
		}
		if !i.cfg.Enabled() || !ok || len(token) < 32 || matched != 1 {
			httputil.WriteError(w, http.StatusUnauthorized, "import credential required")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

type importRequest struct {
	VimeoVideoID    string    `json:"vimeoVideoId"`
	RecordedAt      time.Time `json:"recordedAt"`
	DurationSeconds int       `json:"durationSeconds"`
}

type importResponse struct {
	VideoID      string `json:"videoId"`
	VimeoVideoID string `json:"vimeoVideoId"`
	Created      bool   `json:"created"`
	MediaVersion int    `json:"mediaVersion"`
}

// Import answers 201 for a new video and 200 with the same video for a Vimeo id already
// imported, so a rerun never makes a second one. 409 means the file is not in place yet.
func (i *MPDImporter) Import(w http.ResponseWriter, r *http.Request) {
	var req importRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || !vimeoID.MatchString(req.VimeoVideoID) ||
		req.RecordedAt.IsZero() || req.RecordedAt.After(i.now().Add(time.Hour)) ||
		req.DurationSeconds < 0 || req.DurationSeconds > 24*3600 {
		httputil.WriteError(w, http.StatusBadRequest, "vimeoVideoId, recordedAt and durationSeconds are required")
		return
	}
	ctx := r.Context()
	if existing, err := i.existing(ctx, req.VimeoVideoID); err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to read imports")
		return
	} else if existing != nil {
		httputil.WriteJSON(w, http.StatusOK, existing)
		return
	}
	key := importKey(req.VimeoVideoID)
	size, contentType, err := i.h.storage.HeadObject(ctx, key)
	if err != nil || size <= 0 || contentType != "video/mp4" {
		httputil.WriteError(w, http.StatusConflict, "the imported file is not in place")
		return
	}
	shareToken, err := generateShareToken()
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to generate share token")
		return
	}
	created, err := i.create(ctx, req, key, size, shareToken)
	if errors.Is(err, errImportOwner) {
		httputil.WriteError(w, http.StatusConflict, "the import owner has no MPD staff identity in the workspace")
		return
	}
	if err != nil {
		slog.Error("mpd import: create failed", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "failed to import video")
		return
	}
	if !created.Created {
		httputil.WriteJSON(w, http.StatusOK, created)
		return
	}
	i.enqueue(ctx, JobTypeThumbnail, created.VideoID, map[string]any{
		"thumbnailKey": thumbnailFileKey(i.cfg.OwnerUserID, shareToken),
	})
	i.enqueue(ctx, JobTypeNormalize, created.VideoID, map[string]any{
		"fileKey": key, "audioFilter": "", "duration": req.DurationSeconds,
	})
	if req.DurationSeconds == 0 {
		i.enqueue(ctx, JobTypeProbe, created.VideoID, map[string]any{"fileKey": key})
	}
	httputil.WriteJSON(w, http.StatusCreated, created)
}

var errImportOwner = errors.New("import owner is not a managed staff identity")

func (i *MPDImporter) existing(ctx context.Context, vimeo string) (*importResponse, error) {
	var res importResponse
	err := i.h.db.QueryRow(ctx, `SELECT i.video_id::text, v.media_version FROM mpd_vimeo_imports i
		JOIN videos v ON v.id=i.video_id WHERE i.vimeo_video_id=$1`, vimeo).Scan(&res.VideoID, &res.MediaVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	res.VimeoVideoID = vimeo
	return &res, nil
}

// create inserts the ready video, which the managed-video trigger binds to the owner's
// staff identity and announces to mpd-api with video.ready, and records the Vimeo id.
// A concurrent import of the same Vimeo id loses on the primary key and answers with the
// winner's video.
func (i *MPDImporter) create(ctx context.Context, req importRequest, key string, size int64, share string) (importResponse, error) {
	beginner, ok := i.h.db.(database.Beginner)
	if !ok {
		return importResponse{}, errors.New("import needs a database that can open a transaction")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return importResponse{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var identity bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mpd_external_identities x
		JOIN mpd_managed_workspaces w ON w.organization_id=x.organization_id
		WHERE x.user_id=$1 AND x.organization_id=$2 AND w.enabled)`, i.cfg.OwnerUserID, i.cfg.WorkspaceID).Scan(&identity); err != nil {
		return importResponse{}, err
	}
	if !identity {
		return importResponse{}, errImportOwner
	}
	res := importResponse{VimeoVideoID: req.VimeoVideoID, Created: true}
	if err := tx.QueryRow(ctx, `INSERT INTO videos (user_id, organization_id, title, duration, file_size,
		file_key, share_token, content_type, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'video/mp4', 'ready', $8, now()) RETURNING id::text, media_version`,
		i.cfg.OwnerUserID, i.cfg.WorkspaceID, importTitle, req.DurationSeconds, size, key, share, req.RecordedAt).
		Scan(&res.VideoID, &res.MediaVersion); err != nil {
		return importResponse{}, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO mpd_vimeo_imports (vimeo_video_id, video_id) VALUES ($1, $2)
		ON CONFLICT (vimeo_video_id) DO NOTHING`, req.VimeoVideoID, res.VideoID)
	if err != nil {
		return importResponse{}, err
	}
	if tag.RowsAffected() == 0 {
		_ = tx.Rollback(ctx)
		existing, err := i.existing(ctx, req.VimeoVideoID)
		if err != nil || existing == nil {
			return importResponse{}, errors.New("import raced and the winner is unreadable")
		}
		return *existing, nil
	}
	return res, tx.Commit(ctx)
}
