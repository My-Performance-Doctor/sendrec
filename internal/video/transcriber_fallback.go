package video

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// fallbackTranscriber tries a remote provider first and, when it fails for
// any reason other than silence or the job's own end, transcribes the same
// audio with local Whisper, so a patient's report never waits on a remote
// outage. The remote attempt leaves Whisper a reserve of the job's deadline.
type fallbackTranscriber struct {
	primary  Transcriber
	fallback Transcriber
}

func (f *fallbackTranscriber) Name() string    { return f.primary.Name() }
func (f *fallbackTranscriber) Available() bool { return f.primary.Available() }

func (f *fallbackTranscriber) Transcribe(ctx context.Context, audioPath, language string) ([]TranscriptSegment, error) {
	primaryCtx, cancel := ctx, context.CancelFunc(func() {})
	if deadline, ok := ctx.Deadline(); ok {
		reserve := min(whisperReserve(audioPath), time.Until(deadline)/2)
		primaryCtx, cancel = context.WithDeadline(ctx, deadline.Add(-reserve))
	}
	segments, err := f.primary.Transcribe(primaryCtx, audioPath, language)
	cancel()
	if err == nil {
		slog.Info("transcribe: provider produced the transcript", "provider", f.primary.Name())
		return segments, nil
	}
	// Silence is an answer, and a task that is stopping or out of time has
	// handed the job back already.
	if errors.Is(err, ErrNoAudio) || ctx.Err() != nil || f.fallback == nil || !f.fallback.Available() {
		return nil, err
	}
	slog.Warn("transcribe: provider failed, falling back", "provider", f.primary.Name(), "fallback", f.fallback.Name(), "error", err)
	segments, fallbackErr := f.fallback.Transcribe(ctx, audioPath, language)
	if fallbackErr != nil {
		return nil, fmt.Errorf("%s: %w; %s: %w", f.primary.Name(), err, f.fallback.Name(), fallbackErr)
	}
	slog.Info("transcribe: provider produced the transcript", "provider", f.fallback.Name())
	return segments, nil
}

// whisperReserve is the deadline kept back for local Whisper: five minutes
// plus three times the audio's length, read from the 16 kHz mono 16-bit WAV
// the worker extracts (32,000 bytes a second).
func whisperReserve(audioPath string) time.Duration {
	reserve := 5 * time.Minute
	if info, err := os.Stat(audioPath); err == nil {
		reserve += time.Duration(3*info.Size()/32000) * time.Second
	}
	return reserve
}
