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

func (f *fallbackTranscriber) Name() string { return f.primary.Name() }

// Available is true when either provider can run: a job is worth claiming
// while Whisper alone can do it.
func (f *fallbackTranscriber) Available() bool {
	return f.primary.Available() || f.fallbackAvailable()
}

func (f *fallbackTranscriber) fallbackAvailable() bool {
	return f.fallback != nil && f.fallback.Available()
}

func (f *fallbackTranscriber) Transcribe(ctx context.Context, audioPath, language string) ([]TranscriptSegment, error) {
	if !f.primary.Available() {
		slog.Warn("transcribe: provider unavailable, using the fallback", "provider", f.primary.Name())
		return f.runFallback(ctx, audioPath, language, nil)
	}
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
	if errors.Is(err, ErrNoAudio) || ctx.Err() != nil {
		return nil, err
	}
	if !f.fallbackAvailable() {
		slog.Warn("transcribe: provider failed and the fallback is unavailable", "provider", f.primary.Name(), "error", err)
		return nil, err
	}
	slog.Warn("transcribe: provider failed, falling back", "provider", f.primary.Name(), "fallback", f.fallback.Name(), "error", err)
	return f.runFallback(ctx, audioPath, language, err)
}

// runFallback runs Whisper once no encode holds this task's slot: Whisper
// yields the CPU to encodes, so starting beside one spends the reserve waiting.
func (f *fallbackTranscriber) runFallback(ctx context.Context, audioPath, language string, primaryErr error) ([]TranscriptSegment, error) {
	if !f.fallbackAvailable() {
		if primaryErr == nil {
			primaryErr = errors.New("unavailable")
		}
		return nil, fmt.Errorf("%s: %w; no fallback available", f.primary.Name(), primaryErr)
	}
	if err := waitForEncoderSlot(ctx); err != nil {
		return nil, err
	}
	segments, err := f.fallback.Transcribe(ctx, audioPath, language)
	if err != nil {
		if primaryErr == nil {
			return nil, fmt.Errorf("%s: %w", f.fallback.Name(), err)
		}
		return nil, fmt.Errorf("%s: %w; %s: %w", f.primary.Name(), primaryErr, f.fallback.Name(), err)
	}
	slog.Info("transcribe: provider produced the transcript", "provider", f.fallback.Name())
	return segments, nil
}

// encoderWaitPoll is how often a waiting Whisper run checks the encoder slot.
var encoderWaitPoll = 2 * time.Second

// waitForEncoderSlot waits while every encoder slot on this task is taken,
// for at most half of the time left, so Whisper keeps time to run.
func waitForEncoderSlot(ctx context.Context) error {
	if !encoders().busy() {
		return nil
	}
	limit := time.Now().Add(time.Hour)
	if deadline, ok := ctx.Deadline(); ok {
		limit = time.Now().Add(time.Until(deadline) / 2)
	}
	slog.Info("transcribe: waiting for the running encode before Whisper")
	for encoders().busy() && time.Now().Before(limit) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(encoderWaitPoll):
		}
	}
	return nil
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
