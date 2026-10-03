package video

import (
	"context"
	"errors"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sendrec/sendrec/internal/database"
	"github.com/sendrec/sendrec/internal/httputil"
)

const maxThumbnailUploadBytes = 2 * 1024 * 1024 // 2MB

type thumbnailUploadResponse struct {
	UploadURL string `json:"uploadUrl"`
	// Sent back to CompleteThumbnail once the upload is done. The version is
	// the content the thumbnail was chosen for.
	ThumbnailKey string `json:"thumbnailKey"`
	MediaVersion int    `json:"mediaVersion"`
}

func (h *Handler) UploadThumbnail(w http.ResponseWriter, r *http.Request) {
	videoID := chi.URLParam(r, "id")

	var req struct {
		ContentType   string `json:"contentType"`
		ContentLength int64  `json:"contentLength"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.ContentType != "image/jpeg" && req.ContentType != "image/png" && req.ContentType != "image/webp" {
		httputil.WriteError(w, http.StatusBadRequest, "thumbnail must be JPEG, PNG, or WebP")
		return
	}
	if req.ContentLength <= 0 || req.ContentLength > maxThumbnailUploadBytes {
		httputil.WriteError(w, http.StatusBadRequest, "thumbnail must be 2MB or smaller")
		return
	}

	where, args := orgRowFilter(r.Context(), videoID, nil, "AND status = 'ready'")
	var shareToken string
	var videoOwnerID string
	var version int
	err := h.db.QueryRow(r.Context(),
		`SELECT share_token, user_id, media_version FROM videos WHERE `+where, args...,
	).Scan(&shareToken, &videoOwnerID, &version)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, "video not found")
		return
	}

	// A key of its own, recorded as an attempt: nothing changes for the video
	// until CompleteThumbnail finds the upload there, and an upload that never
	// completes is swept like any other. #327.
	thumbKey := replacementFileKey(thumbnailFileKey(videoOwnerID, shareToken), ".jpg")
	if err := recordReplacementAttempt(r.Context(), h.db, thumbKey); err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to prepare thumbnail upload")
		return
	}

	uploadURL, err := h.storage.GenerateUploadURL(r.Context(), thumbKey, req.ContentType, req.ContentLength, 15*time.Minute)
	if err != nil {
		discardReplacement(r.Context(), h.db, thumbKey)
		httputil.WriteError(w, http.StatusInternalServerError, "failed to generate upload URL")
		return
	}

	httputil.WriteJSON(w, http.StatusOK, thumbnailUploadResponse{UploadURL: uploadURL, ThumbnailKey: thumbKey, MediaVersion: version})
}

var thumbnailUploadTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true}

// CompleteThumbnail publishes a custom thumbnail once the browser's upload
// is done. The thumbnail it replaces stays until then, so a failed or
// abandoned upload leaves the video as it was. It publishes only for the
// content version the upload was authorised for: an edit since then has its
// own thumbnail, and the request gets 409. #327.
func (h *Handler) CompleteThumbnail(w http.ResponseWriter, r *http.Request) {
	videoID := chi.URLParam(r, "id")

	var req struct {
		ThumbnailKey string `json:"thumbnailKey"`
		MediaVersion int    `json:"mediaVersion"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	where, args := orgRowFilter(r.Context(), videoID, nil, "AND status = 'ready'")
	var shareToken, videoOwnerID string
	if err := h.db.QueryRow(r.Context(),
		`SELECT share_token, user_id FROM videos WHERE `+where, args...,
	).Scan(&shareToken, &videoOwnerID); err != nil {
		httputil.WriteError(w, http.StatusNotFound, "video not found")
		return
	}

	// Only a key UploadThumbnail could have minted for this video.
	stem := strings.TrimSuffix(thumbnailFileKey(videoOwnerID, shareToken), ".jpg") + "."
	suffix, ok := strings.CutPrefix(req.ThumbnailKey, stem)
	if !ok || strings.Contains(suffix, "/") || !strings.HasSuffix(suffix, ".jpg") {
		httputil.WriteError(w, http.StatusBadRequest, "invalid thumbnailKey")
		return
	}

	size, contentType, err := h.storage.HeadObject(r.Context(), req.ThumbnailKey)
	if err != nil || size <= 0 || size > maxThumbnailUploadBytes || !thumbnailUploadTypes[contentType] {
		httputil.WriteError(w, http.StatusBadRequest, "thumbnail upload not found")
		return
	}

	// The caller's scope is checked again in the publishing statement: the
	// video may have been transferred while the upload was being checked.
	scope, args := orgRowFilter(r.Context(), videoID, []any{videoID, req.ThumbnailKey, req.MediaVersion}, "")
	published, err := publishUpload(r.Context(), h.db, "thumbnail_key",
		`UPDATE videos SET thumbnail_key = $2, thumbnail_version = $3, updated_at = now()
		 WHERE id = $1 AND `+scope+` AND media_version = $3 AND status != 'deleted'`,
		args...)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to update thumbnail")
		return
	}
	if !published {
		// Not discarded: the upload URL is still valid, and a PUT after an
		// early sweep would leave an object nothing tracks. The attempt
		// record's own deadline outlasts the URL.
		httputil.WriteError(w, http.StatusConflict, "the video changed while the thumbnail was uploading")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ResetThumbnail(w http.ResponseWriter, r *http.Request) {
	videoID := chi.URLParam(r, "id")

	where, args := orgRowFilter(r.Context(), videoID, nil, "AND status = 'ready'")
	var shareToken, fileKey string
	var thumbKey *string
	var videoOwnerID string
	err := h.db.QueryRow(r.Context(),
		`SELECT share_token, file_key, thumbnail_key, user_id FROM videos WHERE `+where, args...,
	).Scan(&shareToken, &fileKey, &thumbKey, &videoOwnerID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, "video not found")
		return
	}

	thumbnailKey := thumbnailFileKey(videoOwnerID, shareToken)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	go func() {
		defer cancel()
		GenerateThumbnail(ctx, h.db, h.storage, videoID, thumbnailKey)
	}()

	w.WriteHeader(http.StatusAccepted)
}

func thumbnailFileKey(userID, shareToken string) string {
	return fmt.Sprintf("recordings/%s/%s.jpg", userID, shareToken)
}

func buildThumbnailArgs(inputPath, outputPath string, seekSeconds int) []string {
	args := append(globalThreads(), inputThreads()...)
	return append(args,
		"-i", inputPath,
		"-ss", fmt.Sprintf("%d", seekSeconds),
		"-frames:v", "1",
		"-vf", "scale=640:-1",
		"-q:v", "5",
		"-y",
		outputPath,
	)
}

// See transcodeToMP4 for why this is a var.
var extractFrameAt = func(ctx context.Context, inputPath, outputPath string, seekSeconds int) error {
	cmd := exec.CommandContext(ctx, "ffmpeg", buildThumbnailArgs(inputPath, outputPath, seekSeconds)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, string(output))
	}
	return nil
}

func extractFrame(ctx context.Context, inputPath, outputPath string) error {
	return extractFrameAt(ctx, inputPath, outputPath, 2)
}

// GenerateThumbnail makes a thumbnail from the video's current file and
// publishes it under a key of its own next to thumbnailBase. It is published
// only for the content it was made from: an edit in the meantime bumps
// media_version and queues its own thumbnail, while a conversion keeps the
// content and the version. #327.
func GenerateThumbnail(ctx context.Context, db database.DBTX, storage ObjectStorage, videoID, thumbnailBase string) {
	var fileKey string
	var version int
	if err := db.QueryRow(ctx,
		`SELECT file_key, media_version FROM videos WHERE id = $1 AND status != 'deleted'`, videoID,
	).Scan(&fileKey, &version); err != nil {
		slog.Info("thumbnail: video gone, skipping", "video_id", videoID, "error", err)
		return
	}
	thumbnailKey := replacementFileKey(thumbnailBase, ".jpg")

	// How this job ends decides what happens to an owed thumbnail: made, given
	// up on (no usable picture), or tried again later (a failure that may
	// pass, or the job's deadline). See recordThumbnailOutcome.
	published, retry := false, false
	defer func() {
		if !published {
			recordThumbnailOutcome(ctx, db, videoID, version, retry)
		}
	}()

	tmpVideo, err := os.CreateTemp("", "sendrec-thumb-*.webm")
	if err != nil {
		slog.Error("thumbnail: failed to create temp video file", "error", err)
		retry = true
		return
	}
	tmpVideoPath := tmpVideo.Name()
	_ = tmpVideo.Close()
	defer func() { _ = os.Remove(tmpVideoPath) }()

	if err := storage.DownloadToFile(ctx, fileKey, tmpVideoPath); err != nil {
		slog.Error("thumbnail: failed to download video", "video_id", videoID, "error", err)
		retry = true
		return
	}

	tmpThumb, err := os.CreateTemp("", "sendrec-thumb-*.jpg")
	if err != nil {
		slog.Error("thumbnail: failed to create temp thumbnail file", "error", err)
		retry = true
		return
	}
	tmpThumbPath := tmpThumb.Name()
	_ = tmpThumb.Close()
	defer func() { _ = os.Remove(tmpThumbPath) }()

	if err := extractFrame(ctx, tmpVideoPath, tmpThumbPath); err != nil {
		slog.Error("thumbnail: ffmpeg failed", "video_id", videoID, "error", err)
		return
	}

	// If -ss 2 produced a 0-byte file (video shorter than 2s), retry at the start
	if info, err := os.Stat(tmpThumbPath); err == nil && info.Size() == 0 {
		slog.Warn("thumbnail: video too short for seek=2, retrying at seek=0", "video_id", videoID)
		if err := extractFrameAt(ctx, tmpVideoPath, tmpThumbPath, 0); err != nil {
			slog.Error("thumbnail: ffmpeg retry failed", "video_id", videoID, "error", err)
			return
		}
	}

	// Skip upload if thumbnail is still empty
	if info, err := os.Stat(tmpThumbPath); err != nil || info.Size() == 0 {
		slog.Warn("thumbnail: no frame extracted, skipping", "video_id", videoID)
		return
	}

	// Published only while the video is live and its content unchanged;
	// anything else is reclaimed by the sweep. #325, #327.
	if err := recordReplacementAttempt(ctx, db, thumbnailKey); err != nil {
		slog.Error("thumbnail: failed to record upload", "video_id", videoID, "error", err)
		retry = true
		return
	}
	if err := storage.UploadFile(ctx, thumbnailKey, tmpThumbPath, "image/jpeg"); err != nil {
		slog.Error("thumbnail: failed to upload", "video_id", videoID, "error", err)
		discardReplacement(ctx, db, thumbnailKey)
		retry = true
		return
	}

	published, err = publishUpload(ctx, db, "thumbnail_key",
		`UPDATE videos SET thumbnail_key = $2, thumbnail_version = $3,
		     thumbnail_attempts = 0, thumbnail_retry_at = NULL, updated_at = now()
		 WHERE id = $1 AND media_version = $3 AND status != 'deleted'`,
		videoID, thumbnailKey, version)
	if err != nil {
		slog.Error("thumbnail: failed to update thumbnail_key", "video_id", videoID, "error", err)
		retry = true
		return
	}
	if !published {
		slog.Info("thumbnail: video deleted or edited meanwhile, discarding", "video_id", videoID)
		discardReplacement(ctx, db, thumbnailKey)
	}
}

// thumbnailJobTimeout bounds one recovery job; maxThumbnailAttempts bounds
// how often one content version is retried. Vars and consts so tests can
// shorten them.
var thumbnailJobTimeout = 5 * time.Minute

const maxThumbnailAttempts = 5

// recordThumbnailOutcome settles a job that made no thumbnail for version,
// if that version still owes one. A shutdown is no attempt: the video is
// released for the next run. A failure that may pass, or the job's
// deadline, is an attempt, retried with backoff up to maxThumbnailAttempts.
// Anything else (no usable picture) gives the version up, and the owner can
// still reset the thumbnail.
func recordThumbnailOutcome(ctx context.Context, db database.DBTX, videoID string, version int, retry bool) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	sql, args := `UPDATE videos SET thumbnail_version = $2, thumbnail_retry_at = NULL
	       WHERE id = $1 AND media_version = $2 AND thumbnail_version < $2`, []any{videoID, version}
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		sql = `UPDATE videos SET thumbnail_retry_at = NULL
		       WHERE id = $1 AND media_version = $2 AND thumbnail_version < $2`
	case retry || errors.Is(ctx.Err(), context.DeadlineExceeded):
		sql = `UPDATE videos SET thumbnail_attempts = thumbnail_attempts + 1,
		           thumbnail_retry_at = now() + make_interval(mins => 10 * power(2, thumbnail_attempts)::int),
		           thumbnail_version = CASE WHEN thumbnail_attempts + 1 >= $3 THEN $2 ELSE thumbnail_version END
		       WHERE id = $1 AND media_version = $2 AND thumbnail_version < $2`
		args = append(args, maxThumbnailAttempts)
	}
	if _, err := db.Exec(writeCtx, sql, args...); err != nil {
		slog.Error("thumbnail: failed to record the outcome", "video_id", videoID, "error", err)
	}
}

// RegenerateMissingThumbnails makes the thumbnails still owed for the
// current content: an edit clears the old one in its switch and makes the
// new one right after, so a process that dies in between leaves the video
// without one. Rows are left alone for a while first, so a job still running
// gets to finish; retries are bounded per version (recordThumbnailOutcome).
// ponytail: scans ready videos every pass; add a partial index on
// (thumbnail_version < media_version) if the videos table gets large.
func RegenerateMissingThumbnails(ctx context.Context, db database.DBTX, storage ObjectStorage) {
	// Claimed with a lease longer than a job, so a run that dies keeps the
	// video back for a while instead of picking it first again; failing
	// videos move behind the rest through thumbnail_retry_at.
	rows, err := db.Query(ctx,
		`UPDATE videos SET thumbnail_retry_at = now() + INTERVAL '30 minutes'
		 WHERE id IN (
		     SELECT id FROM videos
		     WHERE status = 'ready' AND thumbnail_version < media_version
		       AND (thumbnail_retry_at IS NULL OR thumbnail_retry_at <= now())
		       AND updated_at < now() - INTERVAL '10 minutes'
		     ORDER BY thumbnail_retry_at NULLS FIRST, updated_at LIMIT 10
		     FOR UPDATE SKIP LOCKED)
		 RETURNING id, user_id, share_token`)
	if err != nil {
		slog.Error("thumbnail: failed to find missing thumbnails", "error", err)
		return
	}
	type owed struct{ id, userID, shareToken string }
	var videos []owed
	for rows.Next() {
		var v owed
		if err := rows.Scan(&v.id, &v.userID, &v.shareToken); err != nil {
			rows.Close()
			slog.Error("thumbnail: failed to scan missing thumbnail", "error", err)
			return
		}
		videos = append(videos, v)
	}
	rows.Close()
	for _, v := range videos {
		jobCtx, cancel := context.WithTimeout(ctx, thumbnailJobTimeout)
		GenerateThumbnail(jobCtx, db, storage, v.id, thumbnailFileKey(v.userID, v.shareToken))
		cancel()
	}
}
