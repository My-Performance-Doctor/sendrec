package video

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sendrec/sendrec/internal/database"
)

func buildTranscodeArgs(inputPath, outputPath, audioFilter string) []string {
	args := append(globalThreads(), inputThreads()...)
	args = append(args,
		"-i", inputPath,
		"-c:v", "libx264",
		"-profile:v", "high",
		"-level:v", "5.1",
		"-preset", "fast",
		"-crf", "23",
		"-vf", "scale='min(1920,iw)':'min(1080,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2",
		"-r", "60",
	)
	args = appendEncoderBounds(args, "video/mp4")
	if audioFilter != "" {
		args = append(args, "-af", audioFilter)
	}
	args = append(args, "-c:a", "aac", "-movflags", "+faststart", "-y", outputPath)
	return args
}

// Package-level var so tests can reach the steps after ffmpeg without needing
// an ffmpeg binary or a real video fixture.
var transcodeToMP4 = func(ctx context.Context, inputPath, outputPath, audioFilter string) error {
	args := buildTranscodeArgs(inputPath, outputPath, audioFilter)
	// Hold a slot only around ffmpeg itself: the download and upload either
	// side are I/O and would waste the slot.
	release, err := encoders().acquire(ctx)
	if err != nil {
		return err
	}
	defer release()

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg transcode: %w: %s", err, string(output))
	}
	return nil
}

// perJobTimeout bounds a single worker-driven encode. The HTTP-triggered jobs
// already build a 10 minute context each; the periodic workers passed their own
// long-lived context straight through, so before ffmpeg was bound to a context
// that made no difference and now it would mean no deadline at all.
const perJobTimeout = 10 * time.Minute

// maxTranscodeAttempts bounds how often a single video is fed to ffmpeg before
// it is abandoned. Without it a corrupt upload is retried on every worker tick
// forever.
const maxTranscodeAttempts = 5

// permanentFFmpegErrors mark inputs ffmpeg can never decode, so retrying is
// pointless no matter how many attempts remain.
var permanentFFmpegErrors = []string{
	"Invalid data found when processing input",
	"EBML header parsing failed",
	"moov atom not found",
}

func isPermanentFFmpegError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, marker := range permanentFFmpegErrors {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// What the owner is told on the video page when processing fails. They read
// these, so they say what happened to the video and what to do, not why.
const (
	editFailedMessage       = "Your last edit couldn't be applied, so the video is unchanged. Try the edit again."
	conversionFailedMessage = "This video couldn't be converted for playback in every browser. It may not play on some devices; re-uploading it usually helps."
)

// recordTranscodeFailure increments the attempt counter and stores the reason.
// Permanent failures consume the whole budget at once.
//
// The budget belongs to the file the job converted, fileKey. If the video has
// moved on to another file, or was deleted, nothing is counted: an edit's
// output must not inherit the failures of the file it replaced. #328.
func recordTranscodeFailure(ctx context.Context, db database.DBTX, videoID, fileKey string, cause error) {
	permanent := isPermanentFFmpegError(cause)

	// The cause carries raw ffmpeg output, so it can hold arbitrary bytes, and
	// truncation can split a rune. Postgres rejects both invalid UTF-8 and NUL
	// in a text column; either would fail this UPDATE and leave the attempt
	// counter untouched, which is the loop this whole mechanism exists to stop.
	msg := cause.Error()
	if len(msg) > 2000 {
		msg = msg[:2000]
	}
	msg = strings.ReplaceAll(strings.ToValidUTF8(msg, ""), "\x00", "")

	// Written on a context of its own: the commonest failure is the job's
	// deadline, and on the expired job context this write would fail too,
	// leaving the counter untouched and the job retrying forever.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	var attempts int
	err := db.QueryRow(ctx,
		`UPDATE videos
		 SET transcode_attempts = CASE WHEN $3 THEN $4 ELSE transcode_attempts + 1 END,
		     transcode_error = $2,
		     updated_at = now()
		 WHERE id = $1 AND file_key = $5 AND status != 'deleted'
		 RETURNING transcode_attempts`,
		videoID, msg, permanent, maxTranscodeAttempts, fileKey,
	).Scan(&attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		slog.Info("transcode: video moved on from the failed file, not counting it", "video_id", videoID, "key", fileKey, "error", cause)
		return
	}
	if err != nil {
		slog.Error("transcode: failed to record failure", "video_id", videoID, "error", err)
		return
	}

	if attempts >= maxTranscodeAttempts {
		slog.Error("transcode: giving up", "video_id", videoID, "attempts", attempts, "permanent", permanent, "error", cause)
		if _, err := db.Exec(ctx,
			`UPDATE videos SET processing_error = $2 WHERE id = $1 AND file_key = $3`,
			videoID, conversionFailedMessage, fileKey,
		); err != nil {
			slog.Error("transcode: failed to record the give-up for the owner", "video_id", videoID, "error", err)
		}
		return
	}
	slog.Warn("transcode: will retry", "video_id", videoID, "attempts", attempts, "error", cause)
}

// publishConversion uploads a converted copy of the video to newFileKey and
// points the row at it with update, which sets $3 as file_key and $4 as
// file_size. The update must only match while the row is 'ready' and still on
// fileKey ($2): an edit sets the row to 'processing' and commits under a new
// key, so a conversion that would otherwise land during or after it, putting
// pre-edit footage back, yields instead. Its upload is discarded, no attempt
// is spent, and the worker converts the edited file later.
//
// The upload is recorded before it starts and the original is retired by the
// switch, like an edit's (see replaceWithEdit), so neither a crash nor an
// issued URL is a problem.
//
// It reports whether the row was switched. On an error the caller counts an
// attempt.
func publishConversion(ctx context.Context, db database.DBTX, storage ObjectStorage, job, videoID, fileKey, newFileKey, outputPath, update string, newFileSize int64) (bool, error) {
	if err := recordReplacementAttempt(ctx, db, newFileKey); err != nil {
		slog.Error(job+": failed to record output", "video_id", videoID, "error", err)
		return false, err
	}
	if err := storage.UploadFile(ctx, newFileKey, outputPath, "video/mp4"); err != nil {
		slog.Error(job+": failed to upload", "video_id", videoID, "error", err)
		discardReplacement(ctx, db, newFileKey)
		return false, err
	}

	switched, err := switchFileKey(ctx, db, update, videoID, fileKey, newFileKey, newFileSize)
	if err != nil {
		// The switch may have landed; the sweep won't delete a key in use.
		slog.Error(job+": failed to update db", "video_id", videoID, "error", err)
		return false, err
	}
	if !switched {
		slog.Info(job+": video is being edited or changed meanwhile, leaving it to the edit", "video_id", videoID)
		discardReplacement(ctx, db, newFileKey)
	}
	return switched, nil
}

func TranscodeWebMAsync(ctx context.Context, db database.DBTX, storage ObjectStorage, videoID, fileKey, audioFilter string) {
	// Check if video is still WebM (another transcode may have already completed)
	var contentType string
	var attempts int
	var duration int
	if err := db.QueryRow(ctx, "SELECT content_type, transcode_attempts, duration FROM videos WHERE id = $1", videoID).Scan(&contentType, &attempts, &duration); err != nil {
		slog.Error("transcode: failed to check content type", "video_id", videoID, "error", err)
		return
	}
	if contentType != "video/webm" {
		slog.Info("transcode: skipped, already transcoded", "video_id", videoID, "content_type", contentType)
		return
	}
	// The worker query filters on this too, but job enqueues reach us directly.
	if attempts >= maxTranscodeAttempts {
		slog.Warn("transcode: skipped, attempt budget exhausted", "video_id", videoID, "attempts", attempts)
		return
	}

	slog.Info("transcode: starting", "video_id", videoID, "audio_filter", audioFilter)

	tmpInput, err := os.CreateTemp("", "sendrec-transcode-in-*.webm")
	if err != nil {
		slog.Error("transcode: failed to create temp input file", "error", err)
		return
	}
	tmpInputPath := tmpInput.Name()
	_ = tmpInput.Close()
	defer func() { _ = os.Remove(tmpInputPath) }()

	if err := storage.DownloadToFile(ctx, fileKey, tmpInputPath); err != nil {
		slog.Error("transcode: failed to download", "video_id", videoID, "error", err)
		recordTranscodeFailure(ctx, db, videoID, fileKey, err)
		return
	}

	tmpOutput, err := os.CreateTemp("", "sendrec-transcode-out-*.mp4")
	if err != nil {
		slog.Error("transcode: failed to create temp output file", "error", err)
		return
	}
	tmpOutputPath := tmpOutput.Name()
	_ = tmpOutput.Close()
	defer func() { _ = os.Remove(tmpOutputPath) }()

	if err := transcodeToMP4(ctx, tmpInputPath, tmpOutputPath, audioFilter); err != nil {
		slog.Error("transcode: ffmpeg failed", "video_id", videoID, "error", err)
		recordTranscodeFailure(ctx, db, videoID, fileKey, err)
		return
	}

	// The MP4 rather than the WebM that produced it: MediaRecorder's WebM carries
	// no stream durations to compare.
	CheckCapture(ctx, db, videoID, fileKey, tmpOutputPath, duration)

	info, err := os.Stat(tmpOutputPath)
	if err != nil {
		slog.Error("transcode: failed to stat output", "video_id", videoID, "error", err)
		return
	}
	newFileSize := info.Size()

	// A key of its own, not one derived from the WebM's: two runs of this job
	// can overlap, and the one that loses must not delete what the winner
	// switched the row to.
	newFileKey := replacementFileKey(fileKey, ".mp4")

	switched, err := publishConversion(ctx, db, storage, "transcode", videoID, fileKey, newFileKey, tmpOutputPath,
		`UPDATE videos SET file_key = $3, content_type = 'video/mp4', file_size = $4, cues_fixed = true, ios_normalized = true,
		     transcode_attempts = 0, transcode_error = NULL, updated_at = now()
		 WHERE id = $1 AND file_key = $2 AND status = 'ready'`,
		newFileSize)
	if err != nil {
		recordTranscodeFailure(ctx, db, videoID, fileKey, err)
		return
	}
	if !switched {
		return
	}

	slog.Info("transcode: completed", "video_id", videoID, "new_key", newFileKey, "size", newFileSize)
}

func transcodeExistingWebM(ctx context.Context, db database.DBTX, storage ObjectStorage) {
	rows, err := db.Query(ctx,
		`SELECT id, file_key FROM videos
		 WHERE content_type = 'video/webm' AND status = 'ready'
		   AND created_at < now() - interval '5 minutes'
		   AND transcode_attempts < $1
		 ORDER BY created_at DESC LIMIT 50`, maxTranscodeAttempts)
	if err != nil {
		slog.Error("transcode-worker: failed to query", "error", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var videoID, fileKey string
		if err := rows.Scan(&videoID, &fileKey); err != nil {
			slog.Error("transcode-worker: failed to scan", "error", err)
			continue
		}
		jobCtx, cancel := context.WithTimeout(ctx, perJobTimeout)
		TranscodeWebMAsync(jobCtx, db, storage, videoID, fileKey, "")
		cancel()
	}
}

func normalizeExistingVideos(ctx context.Context, db database.DBTX, storage ObjectStorage) {
	rows, err := db.Query(ctx,
		`SELECT id, file_key FROM videos
		 WHERE content_type IN ('video/mp4', 'video/quicktime')
		   AND status = 'ready' AND ios_normalized = false
		   AND created_at < now() - interval '5 minutes'
		   AND transcode_attempts < $1
		 ORDER BY created_at DESC LIMIT 50`, maxTranscodeAttempts)
	if err != nil {
		slog.Error("normalize-worker: failed to query", "error", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var videoID, fileKey string
		if err := rows.Scan(&videoID, &fileKey); err != nil {
			slog.Error("normalize-worker: failed to scan", "error", err)
			continue
		}
		jobCtx, cancel := context.WithTimeout(ctx, perJobTimeout)
		NormalizeVideoAsync(jobCtx, db, storage, videoID, fileKey, "")
		cancel()
	}
}

func StartTranscodeWorker(ctx context.Context, db database.DBTX, storage ObjectStorage, interval time.Duration) {
	go func() {
		transcodeExistingWebM(ctx, db, storage)
		normalizeExistingVideos(ctx, db, storage)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				slog.Info("transcode-worker: shutting down")
				return
			case <-ticker.C:
				transcodeExistingWebM(ctx, db, storage)
				normalizeExistingVideos(ctx, db, storage)
			}
		}
	}()
}
