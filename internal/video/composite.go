package video

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/sendrec/sendrec/internal/database"
)

// Package-level vars so tests can reach the steps around ffmpeg without a binary.
var probeVideoInfo = func(ctx context.Context, path string) (frames int, info string, err error) {
	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-select_streams", "v:0",
		"-count_frames",
		"-show_entries", "stream=nb_read_frames,start_time,codec_name,width,height",
		"-of", "default=noprint_wrappers=1",
		path,
	)
	output, cmdErr := cmd.CombinedOutput()
	if cmdErr != nil {
		return 0, "", fmt.Errorf("ffprobe: %w: %s", cmdErr, string(output))
	}
	info = strings.TrimSpace(string(output))
	for _, line := range strings.Split(info, "\n") {
		if strings.HasPrefix(line, "nb_read_frames=") {
			_, _ = fmt.Sscanf(strings.TrimPrefix(line, "nb_read_frames="), "%d", &frames)
		}
	}
	return frames, info, nil
}

// buildCompositeArgs encodes every composite to H.264 and AAC, whatever the
// screen recording's container. A WebM screen used to go to VP9, which libvpx
// encodes on about one core, only for the transcode worker to re-encode it to
// MP4 afterwards; long recordings ran past the job timeout and were published
// without the webcam. #279.
func buildCompositeArgs(screenPath, webcamPath, outputPath string) []string {
	// PiP filter: scale webcam, add border, normalize timestamps.
	// setpts=PTS-STARTPTS normalizes webcam timestamps to start at 0.
	pipSetup := "[1:v]setpts=PTS-STARTPTS,scale=240:-1,pad=iw+8:ih+8:(ow-iw)/2:(oh-ih)/2:color=black@0.3[pip]"

	// Scale the screen DOWN first, then overlay PiP on top.
	// This ensures the PiP is sized relative to the output resolution,
	// not the original (which may be high-DPI, e.g. 3242x2626).
	filterComplex := "[0:v]scale='min(1920,iw)':'min(1080,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2[screen];" +
		pipSetup + ";[screen][pip]overlay=W-w-20:H-h-20[vout]"
	args := append(globalThreads(), inputThreads()...)
	args = append(args, "-i", screenPath)
	args = append(args, inputThreads()...)
	args = append(args,
		"-i", webcamPath,
		"-filter_complex", filterComplex,
		"-map", "[vout]",
		"-map", "0:a?",
		"-c:v", "libx264",
		"-profile:v", "high",
		"-level:v", "5.1",
		"-preset", "fast",
		"-crf", "23",
		"-r", "60",
		"-c:a", "aac",
		"-movflags", "+faststart",
	)

	// The bounds have to precede the output: ffmpeg applies options to the output
	// that follows them and discards anything after the last one.
	args = appendEncoderBounds(args, "video/mp4")
	return append(args, "-y", outputPath)
}

var compositeOverlay = func(ctx context.Context, screenPath, webcamPath, outputPath string) (string, error) {
	args := buildCompositeArgs(screenPath, webcamPath, outputPath)

	// Hold a slot only around ffmpeg itself: the download and upload either
	// side are I/O and would waste the slot.
	release, err := encoders().acquire(ctx)
	if err != nil {
		return "", err
	}
	defer release()

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("ffmpeg composite: %w: %s", err, string(output))
	}
	return string(output), nil
}

func CompositeWithWebcam(ctx context.Context, db database.DBTX, storage ObjectStorage, videoID, screenKey, webcamKey, thumbnailKey, contentType string) {
	slog.Info("composite: starting webcam overlay", "video_id", videoID)

	setReadyFallback := func() {
		recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := db.Exec(recoveryCtx,
			`UPDATE videos SET status = 'ready', webcam_key = NULL, processing_started_at = NULL, updated_at = now() WHERE id = $1 AND status = 'processing'`,
			videoID,
		); err != nil {
			slog.Error("composite: failed to set fallback ready status", "video_id", videoID, "error", err)
		}
	}

	ext := extensionForContentType(contentType)
	tmpScreen, err := os.CreateTemp("", "sendrec-composite-screen-*"+ext)
	if err != nil {
		slog.Error("composite: failed to create temp screen file", "error", err)
		setReadyFallback()
		return
	}
	tmpScreenPath := tmpScreen.Name()
	_ = tmpScreen.Close()
	defer func() { _ = os.Remove(tmpScreenPath) }()

	if err := storage.DownloadToFile(ctx, screenKey, tmpScreenPath); err != nil {
		slog.Error("composite: failed to download screen", "video_id", videoID, "error", err)
		setReadyFallback()
		return
	}

	tmpWebcam, err := os.CreateTemp("", "sendrec-composite-webcam-*"+ext)
	if err != nil {
		slog.Error("composite: failed to create temp webcam file", "error", err)
		setReadyFallback()
		return
	}
	tmpWebcamPath := tmpWebcam.Name()
	_ = tmpWebcam.Close()
	defer func() { _ = os.Remove(tmpWebcamPath) }()

	if err := storage.DownloadToFile(ctx, webcamKey, tmpWebcamPath); err != nil {
		slog.Error("composite: failed to download webcam", "video_id", videoID, "error", err)
		setReadyFallback()
		return
	}

	// Log file sizes for debugging
	screenInfo, _ := os.Stat(tmpScreenPath)
	webcamInfo, _ := os.Stat(tmpWebcamPath)
	screenSize := int64(0)
	webcamSize := int64(0)
	if screenInfo != nil {
		screenSize = screenInfo.Size()
	}
	if webcamInfo != nil {
		webcamSize = webcamInfo.Size()
	}
	slog.Info("composite: files downloaded", "video_id", videoID, "screen_bytes", screenSize, "webcam_bytes", webcamSize)

	// Verify both inputs have video frames before compositing
	screenFrames, screenProbeInfo, screenProbeErr := probeVideoInfo(ctx, tmpScreenPath)
	if screenProbeErr != nil || screenFrames == 0 {
		slog.Warn("composite: screen has no video frames, skipping overlay", "video_id", videoID, "screen_bytes", screenSize, "probe_error", screenProbeErr)
		setReadyFallback()
		return
	}
	slog.Info("composite: screen validated", "video_id", videoID, "screen_frames", screenFrames, "screen_info", screenProbeInfo)

	// The screen recording rather than the composited output: the webcam overlay
	// spans the full length whatever the screen capture did.
	CheckCapture(ctx, db, videoID, tmpScreenPath, 0)

	webcamFrames, webcamProbeInfo, probeErr := probeVideoInfo(ctx, tmpWebcamPath)
	if probeErr != nil {
		slog.Error("composite: webcam probe failed", "video_id", videoID, "error", probeErr)
		setReadyFallback()
		return
	}
	if webcamFrames == 0 {
		slog.Warn("composite: webcam has no video frames, skipping overlay", "video_id", videoID, "webcam_bytes", webcamSize)
		setReadyFallback()
		return
	}
	slog.Info("composite: webcam validated", "video_id", videoID, "webcam_frames", webcamFrames, "webcam_info", webcamProbeInfo)

	// A WebM screen comes out as MP4 under a new key; MP4 and QuickTime keep
	// theirs.
	outputKey, outputType, outputExt := screenKey, contentType, ext
	if contentType == "video/webm" {
		outputKey, outputType, outputExt = strings.TrimSuffix(screenKey, ".webm")+".mp4", "video/mp4", ".mp4"
	}
	tmpOutput, err := os.CreateTemp("", "sendrec-composite-output-*"+outputExt)
	if err != nil {
		slog.Error("composite: failed to create temp output file", "error", err)
		setReadyFallback()
		return
	}
	tmpOutputPath := tmpOutput.Name()
	_ = tmpOutput.Close()
	defer func() { _ = os.Remove(tmpOutputPath) }()

	ffmpegOutput, err := compositeOverlay(ctx, tmpScreenPath, tmpWebcamPath, tmpOutputPath)
	if err != nil {
		slog.Error("composite: ffmpeg failed", "video_id", videoID, "error", err, "ffmpeg_output", ffmpegOutput)
		setReadyFallback()
		return
	}
	slog.Info("composite: ffmpeg succeeded", "video_id", videoID, "ffmpeg_output", ffmpegOutput)

	if err := storage.UploadFile(ctx, outputKey, tmpOutputPath, outputType); err != nil {
		slog.Error("composite: failed to upload composited video", "video_id", videoID, "error", err)
		setReadyFallback()
		return
	}

	if err := storage.DeleteObject(ctx, webcamKey); err != nil {
		slog.Error("composite: failed to delete webcam file", "key", webcamKey, "error", err)
	}

	if outputKey == screenKey {
		if _, err := db.Exec(ctx,
			`UPDATE videos SET status = 'ready', webcam_key = NULL, processing_started_at = NULL, updated_at = now() WHERE id = $1`,
			videoID,
		); err != nil {
			slog.Error("composite: failed to update status", "video_id", videoID, "error", err)
			return
		}
	} else {
		var fileSize int64
		if info, err := os.Stat(tmpOutputPath); err == nil {
			fileSize = info.Size()
		}
		// Marked as the transcode worker marks its own MP4s, so the worker
		// does not encode this one a second time.
		if _, err := db.Exec(ctx,
			`UPDATE videos SET status = 'ready', webcam_key = NULL, processing_started_at = NULL, file_key = $2, content_type = 'video/mp4', file_size = $3, cues_fixed = true, ios_normalized = true, updated_at = now() WHERE id = $1`,
			videoID, outputKey, fileSize,
		); err != nil {
			slog.Error("composite: failed to update status", "video_id", videoID, "error", err)
			return
		}
		if err := storage.DeleteObject(ctx, screenKey); err != nil {
			slog.Warn("composite: failed to delete original webm", "video_id", videoID, "key", screenKey, "error", err)
		}
	}

	GenerateThumbnail(ctx, db, storage, videoID, outputKey, thumbnailKey)
	if err := EnqueueTranscription(ctx, db, videoID); err != nil {
		slog.Error("composite: failed to enqueue transcription", "video_id", videoID, "error", err)
	}
	slog.Info("composite: completed", "video_id", videoID)
}
