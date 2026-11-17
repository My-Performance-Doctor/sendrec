package video

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Throttling or a network blip while the job runs is retried, not a failure:
// a failed transcript means the patient never gets a report.
func TestAWSTranscriberRetriesFailedPolls(t *testing.T) {
	tr, api, storage, audio := newFakeAWSTranscriber(t, transcribeFixture)
	api.getErrs = 5

	if _, err := tr.Transcribe(context.Background(), audio, "auto"); err != nil {
		t.Fatalf("err = %v, want the transcript after the failed polls", err)
	}
	assertNothingStaged(t, storage, api)
}

func TestAWSTranscriberRetriesAFailedStart(t *testing.T) {
	tr, api, storage, audio := newFakeAWSTranscriber(t, transcribeFixture)
	api.startErrs = 2

	if _, err := tr.Transcribe(context.Background(), audio, "auto"); err != nil {
		t.Fatalf("err = %v, want the transcript on the third start", err)
	}
	if api.startCalls != 3 {
		t.Errorf("start calls = %d, want 3", api.startCalls)
	}
	assertNothingStaged(t, storage, api)
}

// A start whose response was lost created the job anyway: it is used, not
// started twice, and deleted afterwards.
func TestAWSTranscriberUsesAJobCreatedBehindAFailedStart(t *testing.T) {
	tr, api, storage, audio := newFakeAWSTranscriber(t, transcribeFixture)
	api.startErrs, api.createdAnyway = 1, true

	if _, err := tr.Transcribe(context.Background(), audio, "auto"); err != nil {
		t.Fatal(err)
	}
	if api.startCalls != 1 {
		t.Errorf("start calls = %d, want 1", api.startCalls)
	}
	assertNothingStaged(t, storage, api)
}

// A start that keeps failing returns the error, and the job, which a lost
// response may have created, is still deleted.
func TestAWSTranscriberGivesUpOnAStartThatKeepsFailing(t *testing.T) {
	tr, api, storage, audio := newFakeAWSTranscriber(t, transcribeFixture)
	api.startErrs = 99

	_, err := tr.Transcribe(context.Background(), audio, "auto")
	if !errors.Is(err, errSyntheticAWS) {
		t.Fatalf("err = %v, want the start error", err)
	}
	if api.startCalls != tr.startAttempts {
		t.Errorf("start calls = %d, want %d", api.startCalls, tr.startAttempts)
	}
	if len(api.deleted) != 1 {
		t.Errorf("deleted jobs %v, want the one that may exist", api.deleted)
	}
	assertNothingStaged(t, storage, &fakeTranscribe{})
}

// Transcribe cannot cancel a job. One abandoned at its deadline still writes
// its output; cleanup waits for it and deletes the patient's transcript too.
func TestAWSTranscriberDeletesLateOutput(t *testing.T) {
	tr, api, storage, audio := newFakeAWSTranscriber(t, transcribeFixture)
	api.readyAt = time.Now().Add(150 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := tr.Transcribe(ctx, audio, "auto")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline", err)
	}
	if time.Now().Before(api.readyAt) {
		t.Fatal("cleanup returned before the job finished")
	}
	assertNothingStaged(t, storage, api)
}

type stubTranscribeResult struct {
	name     string
	segments []TranscriptSegment
	err      error
	calls    int
	deadline time.Time
}

func (s *stubTranscribeResult) Name() string    { return s.name }
func (s *stubTranscribeResult) Available() bool { return true }
func (s *stubTranscribeResult) Transcribe(ctx context.Context, _, _ string) ([]TranscriptSegment, error) {
	s.calls++
	s.deadline, _ = ctx.Deadline()
	return s.segments, s.err
}

func TestFallbackTranscriber(t *testing.T) {
	audio := filepath.Join(t.TempDir(), "audio.wav")
	if err := os.WriteFile(audio, make([]byte, 32000*60), 0o600); err != nil { // one minute of 16 kHz mono
		t.Fatal(err)
	}
	whisper := []TranscriptSegment{{Start: 0, End: 1, Text: "from whisper"}}

	t.Run("a failed remote job falls back to Whisper", func(t *testing.T) {
		primary := &stubTranscribeResult{name: "aws-transcribe", err: errSyntheticAWS}
		fallback := &stubTranscribeResult{name: "local-whisper", segments: whisper}
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		got, err := (&fallbackTranscriber{primary, fallback}).Transcribe(ctx, audio, "auto")
		if err != nil || len(got) != 1 || got[0].Text != "from whisper" {
			t.Fatalf("got %v, %v", got, err)
		}
		// The remote attempt leaves Whisper 5 min plus 3x the audio's minute.
		deadline, _ := ctx.Deadline()
		if reserve := deadline.Sub(primary.deadline); reserve < 8*time.Minute-time.Second || reserve > 8*time.Minute+time.Second {
			t.Errorf("reserve = %v, want 8 min", reserve)
		}
	})
	t.Run("silence is an answer", func(t *testing.T) {
		primary := &stubTranscribeResult{name: "aws-transcribe", err: ErrNoAudio}
		fallback := &stubTranscribeResult{name: "local-whisper", segments: whisper}
		if _, err := (&fallbackTranscriber{primary, fallback}).Transcribe(context.Background(), audio, "auto"); !errors.Is(err, ErrNoAudio) || fallback.calls != 0 {
			t.Fatalf("err %v, fallback calls %d", err, fallback.calls)
		}
	})
	t.Run("a stopping task does not start Whisper", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		primary := &stubTranscribeResult{name: "aws-transcribe", err: context.Canceled}
		fallback := &stubTranscribeResult{name: "local-whisper", segments: whisper}
		if _, err := (&fallbackTranscriber{primary, fallback}).Transcribe(ctx, audio, "auto"); err == nil || fallback.calls != 0 {
			t.Fatalf("err %v, fallback calls %d", err, fallback.calls)
		}
	})
	t.Run("both failing fails the job", func(t *testing.T) {
		primary := &stubTranscribeResult{name: "aws-transcribe", err: errSyntheticAWS}
		fallback := &stubTranscribeResult{name: "local-whisper", err: errors.New("whisper-cli: killed")}
		_, err := (&fallbackTranscriber{primary, fallback}).Transcribe(context.Background(), audio, "auto")
		if !errors.Is(err, errSyntheticAWS) || !strings.Contains(err.Error(), "whisper-cli") {
			t.Fatalf("err = %v, want both causes", err)
		}
	})
}

// At shutdown the worker's context is cancelled so the running job's provider
// cleans up, and StopTranscription waits for it.
func TestStopTranscriptionCancelsAndWaits(t *testing.T) {
	t.Cleanup(func() { inFlightTranscription.Store(nil); transcriptionCancel.Store(nil) })
	ctx := TranscriptionContext(context.Background())
	inFlightTranscription.Store(&transcriptionJob{videoID: "video-1", generation: 1})
	cleaned := make(chan struct{})
	go func() {
		<-ctx.Done()
		time.Sleep(20 * time.Millisecond) // the provider's cleanup
		close(cleaned)
		inFlightTranscription.Store(nil)
	}()

	StopTranscription(5 * time.Second)

	select {
	case <-cleaned:
	default:
		t.Fatal("StopTranscription returned before the job's cleanup")
	}
}
