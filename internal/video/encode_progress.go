package video

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/sendrec/sendrec/internal/database"
)

// encodeProgressKey carries a conversion's progress reporter on its context,
// so the encoders' package-level test hooks keep their signatures
// (add-mpd-processing-progress).
type encodeProgressKey struct{}

// withEncodeProgress returns ctx with report called for each `out_time_us`
// ffmpeg writes, in microseconds of output written so far.
func withEncodeProgress(ctx context.Context, report func(outMicros int64)) context.Context {
	return context.WithValue(ctx, encodeProgressKey{}, report)
}

func encodeProgressFrom(ctx context.Context) func(int64) {
	report, _ := ctx.Value(encodeProgressKey{}).(func(int64))
	return report
}

// runFFmpeg runs ffmpeg and returns its output for error messages. With a
// reporter on ctx it also asks ffmpeg for `-progress pipe:1` and feeds the
// reporter from it; the returned output is then stderr alone.
func runFFmpeg(ctx context.Context, args []string) ([]byte, error) {
	report := encodeProgressFrom(ctx)
	if report == nil {
		return exec.CommandContext(ctx, "ffmpeg", args...).CombinedOutput()
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", append([]string{"-progress", "pipe:1", "-nostats"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return stderr.Bytes(), err
	}
	scanProgress(stdout, report)
	// A scanner that stopped early must not leave ffmpeg blocked on a full pipe.
	_, _ = io.Copy(io.Discard, stdout)
	err = cmd.Wait()
	return stderr.Bytes(), err
}

// scanProgress reads ffmpeg's `key=value` progress lines. `out_time_us` and,
// despite its name, `out_time_ms` are both microseconds.
func scanProgress(r io.Reader, report func(int64)) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if !ok || (key != "out_time_us" && key != "out_time_ms") {
			continue
		}
		if micros, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && micros >= 0 {
			report(micros)
		}
	}
}

// conversionPercentInterval is how often a conversion writes its percent at
// most, so a long encode does not write on every progress line.
const conversionPercentInterval = 2 * time.Second

// conversionReporter writes the conversion's whole percent of the recording's
// duration to the row, at most every 2 seconds and only when it changes. It
// stops at 99: the conversion is done when its MP4 is published, not when
// ffmpeg reaches the end. Without a duration there is no percent.
func conversionReporter(ctx context.Context, db database.DBTX, videoID, fileKey string, durationSeconds int, now func() time.Time) func(int64) {
	if durationSeconds <= 0 {
		return func(int64) {}
	}
	last, lastAt := -1, time.Time{}
	return func(outMicros int64) {
		percent := int(outMicros / 10_000 / int64(durationSeconds))
		percent = min(max(percent, 0), 99)
		at := now()
		if percent == last || at.Sub(lastAt) < conversionPercentInterval {
			return
		}
		last, lastAt = percent, at
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if _, err := db.Exec(writeCtx,
			`UPDATE videos SET conversion_percent = $3 WHERE id = $1 AND file_key = $2`,
			videoID, fileKey, percent); err != nil {
			slog.Warn("conversion progress: could not record", "video_id", videoID, "error", err)
		}
	}
}
