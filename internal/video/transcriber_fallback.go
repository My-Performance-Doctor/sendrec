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
// outage. The remote attempt gets transcribeBudget of the job's deadline and
// Whisper the rest; without a fallback the remote attempt gets all of it.
//
// Whisper starts at once even while the task encodes: it runs at the lowest
// CPU priority, so it yields to the encode without spending the reserve on a
// wait. A remote job abandoned while still running may complete during its
// cleanup; whichever transcript arrives first is used.
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
		slog.Warn("transcribe: provider unavailable, using the fallback", "video_id", transcriptionVideo(ctx), "provider", f.primary.Name())
		return f.runFallback(ctx, audioPath, language, nil)
	}
	// Without a fallback, Transcribe has the whole deadline.
	primaryCtx, cancel := ctx, context.CancelFunc(func() {})
	if deadline, ok := ctx.Deadline(); ok && f.fallbackAvailable() {
		primaryCtx, cancel = context.WithDeadline(ctx, time.Now().Add(transcribeBudget(audioPath, time.Until(deadline))))
	}
	segments, err := f.primary.Transcribe(primaryCtx, audioPath, language)
	cancel()
	if err == nil {
		slog.Info("transcribe: provider produced the transcript", "video_id", transcriptionVideo(ctx), "provider", f.primary.Name())
		return segments, nil
	}
	// Silence is an answer, and a task that is stopping or out of time has
	// handed the job back already.
	if errors.Is(err, ErrNoAudio) || ctx.Err() != nil {
		return nil, err
	}
	if !f.fallbackAvailable() {
		slog.Warn("transcribe: provider failed and the fallback is unavailable", "video_id", transcriptionVideo(ctx), "provider", f.primary.Name(), "error", err)
		// An abandoned job may still deliver during its cleanup.
		var abandoned *abandonedJobError
		if errors.As(err, &abandoned) {
			select {
			case segments, ok := <-abandoned.late:
				if ok {
					slog.Info("transcribe: provider produced the transcript", "video_id", transcriptionVideo(ctx), "provider", f.primary.Name(), "late", true)
					return segments, nil
				}
			case <-ctx.Done():
			}
		}
		return nil, err
	}
	slog.Warn("transcribe: provider failed, falling back", "video_id", transcriptionVideo(ctx), "provider", f.primary.Name(), "fallback", f.fallback.Name(), "error", err)
	return f.runFallback(ctx, audioPath, language, err)
}

type transcribeResult struct {
	segments []TranscriptSegment
	err      error
}

// runFallback runs Whisper. When the remote job was abandoned while running,
// its late transcript races Whisper: the first transcript wins, and a Whisper
// run that loses is cancelled. The remote job's output is deleted by its own
// cleanup either way.
func (f *fallbackTranscriber) runFallback(ctx context.Context, audioPath, language string, primaryErr error) ([]TranscriptSegment, error) {
	if !f.fallbackAvailable() {
		if primaryErr == nil {
			primaryErr = errors.New("unavailable")
		}
		return nil, fmt.Errorf("%s: %w; no fallback available", f.primary.Name(), primaryErr)
	}
	var late <-chan []TranscriptSegment
	var abandoned *abandonedJobError
	if errors.As(primaryErr, &abandoned) {
		late = abandoned.late
	}
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	whisper := make(chan transcribeResult, 1)
	go func() {
		segments, err := f.fallback.Transcribe(wctx, audioPath, language)
		whisper <- transcribeResult{segments, err}
	}()
	var whisperErr error
	for {
		select {
		case segments, ok := <-late:
			late = nil
			if !ok { // the job settled without a transcript
				if whisper == nil {
					return nil, f.bothFailed(primaryErr, whisperErr)
				}
				continue
			}
			cancel()
			if whisper != nil {
				<-whisper // let Whisper's own cleanup finish
			}
			slog.Info("transcribe: provider produced the transcript", "video_id", transcriptionVideo(ctx), "provider", f.primary.Name(), "late", true)
			return segments, nil
		case r := <-whisper:
			whisper = nil
			if r.err == nil {
				slog.Info("transcribe: provider produced the transcript", "video_id", transcriptionVideo(ctx), "provider", f.fallback.Name())
				return r.segments, nil
			}
			whisperErr = r.err
			if late == nil {
				return nil, f.bothFailed(primaryErr, whisperErr)
			}
			// Whisper failed; the abandoned job may still deliver.
		}
	}
}

func (f *fallbackTranscriber) bothFailed(primaryErr, whisperErr error) error {
	if primaryErr == nil {
		return fmt.Errorf("%s: %w", f.fallback.Name(), whisperErr)
	}
	return fmt.Errorf("%s: %w; %s: %w", f.primary.Name(), primaryErr, f.fallback.Name(), whisperErr)
}

// transcribeBudget is the share of the remaining deadline the remote attempt
// gets: ten minutes plus half the audio's length, and never more than half.
// Transcribe finishes a 20-minute recording in under a minute, so this is
// ample. Whisper keeps the rest, about 20 minutes plus 3.5 times the length of
// the 30 minutes plus 4 times it, which covers dense speech on a slow host
// beside an encode at nice 19. The length is read from the 16 kHz mono 16-bit
// WAV the worker extracts (32,000 bytes a second).
func transcribeBudget(audioPath string, remaining time.Duration) time.Duration {
	budget := 10 * time.Minute
	if info, err := os.Stat(audioPath); err == nil {
		budget += time.Duration(info.Size()/32000/2) * time.Second
	}
	return min(budget, remaining/2)
}
