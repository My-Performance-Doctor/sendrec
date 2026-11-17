package video

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/smithy-go"
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

// A start that keeps failing returns the error. When the probe proves no job
// exists there is nothing to delete; when a lost response created one, it is
// deleted.
func TestAWSTranscriberGivesUpOnAStartThatKeepsFailing(t *testing.T) {
	for _, created := range []bool{false, true} {
		tr, api, storage, audio := newFakeAWSTranscriber(t, transcribeFixture)
		api.startErrs = 99
		if created {
			// The job exists but cannot be read: the probe is throttled too.
			api.createdAnyway, api.getErrs = true, 1<<30
		}

		_, err := tr.Transcribe(context.Background(), audio, "auto")
		if !errors.Is(err, errSyntheticAWS) {
			t.Fatalf("created %v: err = %v, want the start error", created, err)
		}
		transcribeCleanups.Wait()
		api.mu.Lock()
		starts, deleted := api.startCalls, len(api.deleted)
		api.mu.Unlock()
		if starts != tr.startAttempts {
			t.Errorf("created %v: start calls = %d, want %d", created, starts, tr.startAttempts)
		}
		if want := map[bool]int{false: 0, true: 1}[created]; deleted != want {
			t.Errorf("created %v: deleted %d jobs, want %d", created, deleted, want)
		}
		assertNothingStaged(t, storage, &fakeTranscribe{})
	}
}

// Transcribe cannot cancel a job. One abandoned at its deadline still writes
// its output; a background cleanup waits for it and deletes the patient's
// transcript too, while Transcribe returns at once so a fallback keeps its
// whole reserve.
func TestAWSTranscriberDeletesLateOutput(t *testing.T) {
	tr, api, storage, audio := newFakeAWSTranscriber(t, transcribeFixture)
	api.readyAt = time.Now().Add(150 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := tr.Transcribe(ctx, audio, "auto")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline", err)
	}
	if !time.Now().Before(api.readyAt) {
		t.Fatal("Transcribe waited for the abandoned job instead of returning")
	}
	storage.mu.Lock()
	_, audioLeft := storage.objects[strings.TrimPrefix(aws.ToString(api.started.Media.MediaFileUri), "s3://recordings/")]
	storage.mu.Unlock()
	if audioLeft {
		t.Error("the patient's audio was not deleted before returning")
	}
	assertNothingStaged(t, storage, api)
	if time.Now().Before(api.readyAt) {
		t.Fatal("cleanup finished before the job wrote its output")
	}
}

// A shutdown during a settle cuts it to the stopping bound, and the output
// and job are still deleted.
func TestAWSTranscriberSettleShortensAtShutdown(t *testing.T) {
	tr, api, storage, audio := newFakeAWSTranscriber(t, transcribeFixture)
	tr.settle = time.Minute
	prev := stoppingSettle
	stoppingSettle = 50 * time.Millisecond
	t.Cleanup(func() { stoppingSettle = prev; transcriptionStopping.Store(false) })
	api.polls = 1 << 30
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if _, err := tr.Transcribe(ctx, audio, "auto"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	began := time.Now()
	transcriptionStopping.Store(true)
	assertNothingStaged(t, storage, api)
	if waited := time.Since(began); waited > 2*time.Second {
		t.Errorf("settle took %v after shutdown began, want about the stopping bound", waited)
	}
}

// Access and validation errors will not clear, so the job goes to the
// fallback at once instead of spending the deadline retrying.
func TestAWSTranscriberFailsFastOnPermanentErrors(t *testing.T) {
	t.Run("start", func(t *testing.T) {
		tr, api, storage, audio := newFakeAWSTranscriber(t, transcribeFixture)
		api.startErrs, api.startErr = 99, errAccessDenied
		_, err := tr.Transcribe(context.Background(), audio, "auto")
		assertNothingStaged(t, storage, &fakeTranscribe{})
		api.mu.Lock()
		starts := api.startCalls
		api.mu.Unlock()
		if !errors.Is(err, errAccessDenied) || starts != 1 {
			t.Fatalf("err %v after %d starts, want access denied after one", err, starts)
		}
	})
	t.Run("poll", func(t *testing.T) {
		// With no deadline, a retried error would never return: returning at
		// all is the fail-fast.
		tr, api, storage, audio := newFakeAWSTranscriber(t, transcribeFixture)
		api.getErrs, api.getErr = 99, errAccessDenied
		if _, err := tr.Transcribe(context.Background(), audio, "auto"); !errors.Is(err, errAccessDenied) {
			t.Fatalf("err = %v, want access denied", err)
		}
		transcribeCleanups.Wait()
		api.mu.Lock()
		api.getErrs = 0
		api.mu.Unlock()
		assertNothingStaged(t, storage, api)
	})
}

func TestRetryableTranscribeError(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{errSyntheticAWS, true},
		{&smithy.GenericAPIError{Code: "LimitExceededException"}, true},
		{&smithy.GenericAPIError{Code: "InternalFailureException", Fault: smithy.FaultServer}, true},
		{errors.New("dial tcp: connection reset"), true},
		{errAccessDenied, false},
		{errJobNotFound, false},
		{&smithy.GenericAPIError{Code: "ValidationException"}, false},
	} {
		if got := retryableTranscribeError(tc.err); got != tc.want {
			t.Errorf("%v: retryable %v, want %v", tc.err, got, tc.want)
		}
	}
	if !jobMissing(errJobNotFound) || jobMissing(errAccessDenied) {
		t.Error("jobMissing must match the API's missing-job BadRequestException only")
	}
}

type stubTranscribeResult struct {
	name        string
	segments    []TranscriptSegment
	err         error
	calls       int
	deadline    time.Time
	unavailable bool
	calledAt    time.Time
}

func (s *stubTranscribeResult) Name() string    { return s.name }
func (s *stubTranscribeResult) Available() bool { return !s.unavailable }
func (s *stubTranscribeResult) Transcribe(ctx context.Context, _, _ string) ([]TranscriptSegment, error) {
	s.calls++
	s.calledAt = time.Now()
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

func TestFallbackTranscriberAvailability(t *testing.T) {
	whisper := []TranscriptSegment{{Start: 0, End: 1, Text: "from whisper"}}
	t.Run("an unavailable remote provider goes straight to Whisper", func(t *testing.T) {
		primary := &stubTranscribeResult{name: "aws-transcribe", unavailable: true}
		fallback := &stubTranscribeResult{name: "local-whisper", segments: whisper}
		f := &fallbackTranscriber{primary, fallback}
		if !f.Available() {
			t.Fatal("available while Whisper can run")
		}
		got, err := f.Transcribe(context.Background(), "audio.wav", "auto")
		if err != nil || len(got) != 1 || primary.calls != 0 {
			t.Fatalf("got %v, %v, primary calls %d", got, err, primary.calls)
		}
	})
	t.Run("no fallback leaves the remote error", func(t *testing.T) {
		primary := &stubTranscribeResult{name: "aws-transcribe", err: errSyntheticAWS}
		fallback := &stubTranscribeResult{name: "local-whisper", unavailable: true}
		f := &fallbackTranscriber{primary, fallback}
		if _, err := f.Transcribe(context.Background(), "audio.wav", "auto"); !errors.Is(err, errSyntheticAWS) || fallback.calls != 0 {
			t.Fatalf("err %v, fallback calls %d", err, fallback.calls)
		}
	})
	t.Run("neither available", func(t *testing.T) {
		f := &fallbackTranscriber{&stubTranscribeResult{unavailable: true}, &stubTranscribeResult{unavailable: true}}
		if f.Available() {
			t.Fatal("available with neither provider")
		}
	})
}
