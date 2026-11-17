package video

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/transcribe/types"
)

// A ConflictException on a retried start means an earlier attempt created the
// job: it is waited for and used, not abandoned.
func TestAWSTranscriberConflictMeansTheJobExists(t *testing.T) {
	tr, api, storage, audio := newFakeAWSTranscriber(t, transcribeFixture)
	api.startErrs, api.createdAnyway = 1, true
	api.startErr = &types.ConflictException{Message: aws.String("The requested job name already exists.")}

	if _, err := tr.Transcribe(context.Background(), audio, "auto"); err != nil {
		t.Fatalf("err = %v, want the job's transcript", err)
	}
	api.mu.Lock()
	starts := api.startCalls
	api.mu.Unlock()
	if starts != 1 {
		t.Errorf("start calls = %d, want 1", starts)
	}
	assertNothingStaged(t, storage, api)
}

// A probe that finds no job just after the first start attempt cannot prove
// there is none: the job is still settled and deleted.
func TestAWSTranscriberProbeWithinTheGraceKeepsTheJob(t *testing.T) {
	tr, api, _, audio := newFakeAWSTranscriber(t, transcribeFixture)
	api.startErrs = 99 // throttled every time; the probe answers missing

	if _, err := tr.Transcribe(context.Background(), audio, "auto"); err == nil {
		t.Fatal("want the start error")
	}
	transcribeCleanups.Wait()
	api.mu.Lock()
	deleted := len(api.deleted)
	api.mu.Unlock()
	if deleted != 1 {
		t.Errorf("deleted %d jobs, want the one that may exist", deleted)
	}
}

// Without a fallback, the remote attempt gets the whole deadline and an
// abandoned job's late transcript is still used.
func TestFallbackUnavailableGivesTranscribeEverything(t *testing.T) {
	t.Run("whole deadline", func(t *testing.T) {
		primary := &stubTranscribeResult{name: "aws-transcribe", segments: []TranscriptSegment{{Text: "x"}}}
		f := &fallbackTranscriber{primary, &stubTranscribeResult{unavailable: true}}
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		if _, err := f.Transcribe(ctx, "audio.wav", "auto"); err != nil {
			t.Fatal(err)
		}
		if want, _ := ctx.Deadline(); !primary.deadline.Equal(want) {
			t.Errorf("remote deadline %v, want the job's %v", primary.deadline, want)
		}
	})
	t.Run("late result", func(t *testing.T) {
		f, api, storage, audio, _ := abandonedThenCompletes(t, 30*time.Millisecond)
		f.fallback = &stubTranscribeResult{unavailable: true}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		got, err := f.Transcribe(ctx, audio, "auto")
		if err != nil || len(got) != 2 {
			t.Fatalf("got %+v, %v, want the late transcript", got, err)
		}
		assertNothingStaged(t, storage, api)
	})
}

func TestTranscribeBudget(t *testing.T) {
	audio := filepath.Join(t.TempDir(), "audio.wav")
	if err := os.WriteFile(audio, make([]byte, 32000*600), 0o600); err != nil { // ten minutes
		t.Fatal(err)
	}
	// A 10-minute recording's deadline is 70 minutes: Transcribe gets 15,
	// Whisper keeps 55, which is 20 minutes plus 3.5 times the length.
	if got := transcribeBudget(audio, 70*time.Minute); got != 15*time.Minute {
		t.Errorf("budget = %v, want 15m", got)
	}
	// Never more than half of what is left.
	if got := transcribeBudget(audio, 20*time.Minute); got != 10*time.Minute {
		t.Errorf("budget = %v, want 10m", got)
	}
	if got := transcribeBudget(filepath.Join(t.TempDir(), "missing.wav"), time.Hour); got != 10*time.Minute {
		t.Errorf("budget without audio = %v, want 10m", got)
	}
}
