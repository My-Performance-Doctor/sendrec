package video

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"time"

	"github.com/sendrec/sendrec/internal/database"
)

func videoCodecForContentType(ct string) string {
	switch ct {
	case "video/mp4", "video/quicktime":
		return "libx264"
	default:
		return "libvpx-vp9"
	}
}

func buildTrimArgs(inputPath, outputPath, contentType string, startSeconds, endSeconds float64) []string {
	var args []string
	if contentType == "video/mp4" || contentType == "video/quicktime" {
		// iOS-safe encoding: constrain resolution, set profile/level, transcode audio to AAC
		args = append(globalThreads(), inputThreads()...)
		args = append(args,
			"-i", inputPath,
			"-ss", fmt.Sprintf("%.3f", startSeconds),
			"-to", fmt.Sprintf("%.3f", endSeconds),
			"-c:v", "libx264",
			"-profile:v", "high",
			"-level:v", "5.1",
			"-preset", "fast",
			"-crf", "23",
			"-vf", "scale='min(1920,iw)':'min(1080,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2",
			"-r", "60",
			"-c:a", "aac",
			"-movflags", "+faststart",
		)
	} else {
		args = append(globalThreads(), inputThreads()...)
		args = append(args,
			"-i", inputPath,
			"-ss", fmt.Sprintf("%.3f", startSeconds),
			"-to", fmt.Sprintf("%.3f", endSeconds),
			"-c:v", "libvpx-vp9",
			"-c:a", "copy",
		)
	}

	// The bounds have to precede the output: ffmpeg applies options to the output
	// that follows them and discards anything after the last one.
	args = appendEncoderBounds(args, contentType)
	return append(args, "-y", outputPath)
}

// See transcodeToMP4 for why this is a var.
var trimVideo = func(ctx context.Context, inputPath, outputPath, contentType string, startSeconds, endSeconds float64) error {
	args := buildTrimArgs(inputPath, outputPath, contentType, startSeconds, endSeconds)

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
		return fmt.Errorf("ffmpeg trim: %w: %s", err, string(output))
	}
	return nil
}

// replaceWithEdit makes the edited file at outputPath the video. It is stored
// under a new key, recorded before the upload so a crash can't orphan it. One
// statement then switches the row to that key with the new duration and size,
// and retires the original: it stays for the lifetime of the URLs already
// handed out for it, and the cleanup loop deletes it after that. A crash at any
// point leaves the row describing an object that exists.
//
// The switch only applies to the row this edit claimed: still 'processing' and
// still on fileKey. Otherwise the upload belongs to nobody and is discarded.
//
// It reports false when the edit was not applied; the caller then releases
// its claim.
func replaceWithEdit(ctx context.Context, db database.DBTX, storage ObjectStorage, job, videoID, fileKey, thumbnailKey, contentType, outputPath string, newDuration int) bool {
	info, err := os.Stat(outputPath)
	if err != nil {
		slog.Error(job+": failed to stat edited video", "video_id", videoID, "error", err)
		return false
	}

	newKey := replacementFileKey(fileKey, extensionForContentType(contentType))
	if err := recordReplacementAttempt(ctx, db, newKey); err != nil {
		slog.Error(job+": failed to record edited video", "video_id", videoID, "error", err)
		return false
	}
	if err := storage.UploadFile(ctx, newKey, outputPath, contentType); err != nil {
		slog.Error(job+": failed to upload edited video", "video_id", videoID, "error", err)
		discardReplacement(ctx, db, newKey)
		return false
	}

	switched, err := switchFileKey(ctx, db,
		`UPDATE videos SET file_key = $3, duration = $4, file_size = $5, status = 'ready',
		     processing_started_at = NULL, processing_error = NULL, updated_at = now()
		 WHERE id = $1 AND file_key = $2 AND status = 'processing'`,
		videoID, fileKey, newKey, newDuration, info.Size(),
	)
	if err != nil {
		// The switch may have landed even so. The upload stays recorded, and the
		// sweep only deletes it if no video points at it.
		slog.Error(job+": failed to switch to edited video", "video_id", videoID, "new_key", newKey, "error", err)
		return false
	}
	if !switched {
		slog.Warn(job+": video changed during the edit, discarding it", "video_id", videoID, "new_key", newKey)
		discardReplacement(ctx, db, newKey)
		return false
	}

	// Re-checked, not cleared: cutting off a dead tail fixes the recording, and
	// cutting elsewhere leaves it as broken as it was.
	CheckCapture(ctx, db, videoID, newKey, outputPath, newDuration)

	GenerateThumbnail(ctx, db, storage, videoID, newKey, thumbnailKey)
	if err := EnqueueTranscription(ctx, db, videoID); err != nil {
		slog.Error(job+": failed to enqueue transcription", "video_id", videoID, "error", err)
	}
	return true
}

func TrimVideoAsync(ctx context.Context, db database.DBTX, storage ObjectStorage, videoID, fileKey, thumbnailKey, contentType string, startSeconds, endSeconds float64) {
	slog.Info("trim: starting", "video_id", videoID, "start_seconds", startSeconds, "end_seconds", endSeconds)

	setReadyFallback := func() {
		recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := db.Exec(recoveryCtx,
			`UPDATE videos SET status = 'ready', processing_started_at = NULL, processing_error = $2, updated_at = now() WHERE id = $1 AND file_key = $3 AND status = 'processing'`,
			videoID, editFailedMessage, fileKey,
		); err != nil {
			slog.Error("trim: failed to set fallback ready status", "video_id", videoID, "error", err)
		}
	}

	ext := extensionForContentType(contentType)
	tmpInput, err := os.CreateTemp("", "sendrec-trim-input-*"+ext)
	if err != nil {
		slog.Error("trim: failed to create temp input file", "error", err)
		setReadyFallback()
		return
	}
	tmpInputPath := tmpInput.Name()
	_ = tmpInput.Close()
	defer func() { _ = os.Remove(tmpInputPath) }()

	if err := storage.DownloadToFile(ctx, fileKey, tmpInputPath); err != nil {
		slog.Error("trim: failed to download video", "video_id", videoID, "error", err)
		setReadyFallback()
		return
	}

	tmpOutput, err := os.CreateTemp("", "sendrec-trim-output-*"+ext)
	if err != nil {
		slog.Error("trim: failed to create temp output file", "error", err)
		setReadyFallback()
		return
	}
	tmpOutputPath := tmpOutput.Name()
	_ = tmpOutput.Close()
	defer func() { _ = os.Remove(tmpOutputPath) }()

	if err := trimVideo(ctx, tmpInputPath, tmpOutputPath, contentType, startSeconds, endSeconds); err != nil {
		slog.Error("trim: ffmpeg failed", "video_id", videoID, "error", err)
		setReadyFallback()
		return
	}

	if !replaceWithEdit(ctx, db, storage, "trim", videoID, fileKey, thumbnailKey, contentType, tmpOutputPath, int(endSeconds-startSeconds)) {
		setReadyFallback()
		return
	}
	slog.Info("trim: completed", "video_id", videoID)
}
