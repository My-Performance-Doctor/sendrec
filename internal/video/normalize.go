package video

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"

	"github.com/sendrec/sendrec/internal/database"
)

const (
	iOSMaxWidth  = 1920
	iOSMaxHeight = 1080
	iOSMaxLevel  = 51
	iOSMaxFPS    = 60
)

type videoProperties struct {
	Width     int
	Height    int
	Rotation  int // degrees from the display matrix; ffmpeg applies it before any filter
	Level     int
	FrameRate float64
	CodecName string
	CodecTag  string
}

func (p videoProperties) needsNormalization() bool {
	if p.CodecName != "h264" {
		return true
	}
	// avc3 carries its parameter sets in-band, so the header's size holds for
	// the first frame only: a browser take that went full screen changes size
	// part way through. Re-encoding gives it one frame and a plain avc1 track.
	if p.CodecTag == "avc3" {
		return true
	}
	if p.Width > iOSMaxWidth || p.Height > iOSMaxHeight {
		return true
	}
	if p.Level > iOSMaxLevel {
		return true
	}
	if p.FrameRate > float64(iOSMaxFPS) {
		return true
	}
	return false
}

func parseFrameRate(raw string) float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	parts := strings.SplitN(raw, "/", 2)
	if len(parts) != 2 {
		f, _ := strconv.ParseFloat(raw, 64)
		return f
	}
	num, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0
	}
	den, err := strconv.ParseFloat(parts[1], 64)
	if err != nil || den == 0 {
		return 0
	}
	return num / den
}

type ffprobeResult struct {
	Streams []struct {
		Width      int    `json:"width"`
		Height     int    `json:"height"`
		Level      int    `json:"level"`
		RFrameRate string `json:"r_frame_rate"`
		CodecName  string `json:"codec_name"`
		CodecTag   string `json:"codec_tag_string"`
		SideData   []struct {
			Rotation int `json:"rotation"`
		} `json:"side_data_list"`
	} `json:"streams"`
}

// See transcodeToMP4 for why this is a var.
var probeVideoProperties = func(ctx context.Context, inputPath string) (videoProperties, error) {
	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height,level,r_frame_rate,codec_name,codec_tag_string:stream_side_data=rotation",
		"-of", "json",
		inputPath,
	)
	output, err := cmd.Output()
	if err != nil {
		return videoProperties{}, fmt.Errorf("ffprobe: %w", err)
	}

	var result ffprobeResult
	if err := json.Unmarshal(output, &result); err != nil {
		return videoProperties{}, fmt.Errorf("ffprobe parse: %w", err)
	}

	if len(result.Streams) == 0 {
		return videoProperties{}, fmt.Errorf("ffprobe: no video streams found")
	}

	s := result.Streams[0]
	// Rotation is the display matrix side data. ffprobe 6.1 in the image and 9
	// locally both report it there and neither reports the legacy rotate tag.
	var rotation int
	for _, sd := range s.SideData {
		if sd.Rotation != 0 {
			rotation = sd.Rotation
			break
		}
	}
	return videoProperties{
		Width:     s.Width,
		Height:    s.Height,
		Rotation:  rotation,
		Level:     s.Level,
		FrameRate: parseFrameRate(s.RFrameRate),
		CodecName: s.CodecName,
		CodecTag:  s.CodecTag,
	}, nil
}

func buildNormalizeArgs(inputPath, outputPath, audioFilter string, frame outputFrame) []string {
	args := append(globalThreads(), inputThreads()...)
	args = append(args,
		"-i", inputPath,
		"-c:v", "libx264",
		"-profile:v", "high",
		"-level:v", "5.1",
		"-preset", "fast",
		"-crf", "23",
		"-vf", frame.filter(),
		"-r", "60",
	)
	args = appendEncoderBounds(args, "video/mp4")
	if audioFilter != "" {
		args = append(args, "-af", audioFilter)
	}
	args = append(args, "-c:a", "aac", "-movflags", "+faststart", "-y", outputPath)
	return args
}

// See transcodeToMP4 for why this is a var.
var transcodeToIOSCompatible = func(ctx context.Context, inputPath, outputPath, audioFilter string) error {
	// The caller probed this file to decide on the encode; sizing the frame
	// here as well keeps the four encoders alike and the probe is cheap.
	args := buildNormalizeArgs(inputPath, outputPath, audioFilter, probeOutputFrame(ctx, inputPath))
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
		return fmt.Errorf("ffmpeg normalize: %w: %s", err, string(output))
	}
	return nil
}

func NormalizeVideoAsync(ctx context.Context, db database.DBTX, storage ObjectStorage, videoID, fileKey, audioFilter string) {
	// Check if video is already normalized (another normalize may have completed)
	var normalized bool
	var attempts int
	var duration int
	if err := db.QueryRow(ctx, "SELECT ios_normalized, transcode_attempts, duration FROM videos WHERE id = $1", videoID).Scan(&normalized, &attempts, &duration); err != nil {
		slog.Error("normalize: failed to check status", "video_id", videoID, "error", err)
		return
	}
	if normalized {
		slog.Info("normalize: skipped, already normalized", "video_id", videoID)
		return
	}
	// The worker query filters on this too, but job enqueues reach us directly.
	if attempts >= maxTranscodeAttempts {
		slog.Warn("normalize: skipped, attempt budget exhausted", "video_id", videoID, "attempts", attempts)
		return
	}

	slog.Info("normalize: starting", "video_id", videoID, "audio_filter", audioFilter)

	tmpInput, err := os.CreateTemp("", "sendrec-normalize-in-*.mp4")
	if err != nil {
		slog.Error("normalize: failed to create temp input file", "error", err)
		return
	}
	tmpInputPath := tmpInput.Name()
	_ = tmpInput.Close()
	defer func() { _ = os.Remove(tmpInputPath) }()

	if err := storage.DownloadToFile(ctx, fileKey, tmpInputPath); err != nil {
		slog.Error("normalize: failed to download", "video_id", videoID, "error", err)
		recordTranscodeFailure(ctx, db, videoID, fileKey, err)
		return
	}

	// The file is already here and it is the recording as uploaded, so this is
	// where a capture that died gets noticed — and where the warning clears if a
	// later edit fixed it.
	CheckCapture(ctx, db, videoID, fileKey, tmpInputPath, duration)

	props, err := probeVideoProperties(ctx, tmpInputPath)
	if err != nil {
		// A failed probe says nothing about compatibility. Marking the video
		// normalized here took it out of every later pass while it could still
		// fail on iPhones; count an attempt instead, within the retry budget.
		slog.Warn("normalize: probe failed", "video_id", videoID, "error", err)
		recordTranscodeFailure(ctx, db, videoID, fileKey, err)
		return
	}

	if !props.needsNormalization() {
		slog.Info("normalize: already compatible", "video_id", videoID,
			"width", props.Width, "height", props.Height, "level", props.Level, "fps", props.FrameRate)
		markIOSNormalized(ctx, db, videoID, fileKey)
		return
	}

	slog.Info("normalize: re-encoding", "video_id", videoID,
		"width", props.Width, "height", props.Height, "level", props.Level,
		"fps", props.FrameRate, "codec", props.CodecName, "tag", props.CodecTag)

	tmpOutput, err := os.CreateTemp("", "sendrec-normalize-out-*.mp4")
	if err != nil {
		slog.Error("normalize: failed to create temp output file", "error", err)
		return
	}
	tmpOutputPath := tmpOutput.Name()
	_ = tmpOutput.Close()
	defer func() { _ = os.Remove(tmpOutputPath) }()

	if err := transcodeToIOSCompatible(ctx, tmpInputPath, tmpOutputPath, audioFilter); err != nil {
		slog.Error("normalize: ffmpeg failed", "video_id", videoID, "error", err)
		recordTranscodeFailure(ctx, db, videoID, fileKey, err)
		return
	}

	info, err := os.Stat(tmpOutputPath)
	if err != nil {
		slog.Error("normalize: failed to stat output", "video_id", videoID, "error", err)
		return
	}
	newFileSize := info.Size()

	// Written next to the original, never over it: an edit may be reading it.
	newFileKey := replacementFileKey(fileKey, path.Ext(fileKey))
	switched, err := publishConversion(ctx, db, storage, "normalize", videoID, fileKey, newFileKey, tmpOutputPath,
		`UPDATE videos SET file_key = $3, file_size = $4, ios_normalized = true,
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

	slog.Info("normalize: completed", "video_id", videoID, "new_size", newFileSize)
}

// The verdict is about the file that was probed, so it only lands while the
// row still points at it and no edit is replacing it.
func markIOSNormalized(ctx context.Context, db database.DBTX, videoID, fileKey string) {
	if _, err := db.Exec(ctx,
		`UPDATE videos SET ios_normalized = true, updated_at = now() WHERE id = $1 AND file_key = $2 AND status = 'ready'`,
		videoID, fileKey,
	); err != nil {
		slog.Error("normalize: failed to mark normalized", "video_id", videoID, "error", err)
	}
}
