package video

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v5"
)

// statusWriteDB counts writes and fails any made on a context that has ended.
type statusWriteDB struct {
	pgxmock.PgxPoolIface
	writes atomic.Int32
}

func (d *statusWriteDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	d.writes.Add(1)
	if err := ctx.Err(); err != nil {
		return pgconn.CommandTag{}, err
	}
	return d.PgxPoolIface.Exec(ctx, sql, args...)
}

// A job stopped at shutdown writes nothing: the hand-back already returned it
// to the queue, and 'failed' would strand the patient's report.
func TestProcessTranscriptionWritesNothingWhenStopped(t *testing.T) {
	t.Setenv("TRANSCRIPTION_ENABLED", "true")
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	db := &statusWriteDB{PgxPoolIface: mock}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	processTranscription(ctx, db, newMemStorage(map[string]string{}), stubTranscriber{available: true},
		"video-1", "recordings/missing.webm", 1, 60, "user-1", "share", "auto", false, 2)

	if n := db.writes.Load(); n != 0 {
		t.Errorf("%d status writes after a shutdown stop, want none", n)
	}
}

// A job that runs out of time still records its failure, on a context that
// outlives the expired job context. BG-07.
func TestProcessTranscriptionRecordsFailureAtItsDeadline(t *testing.T) {
	t.Setenv("TRANSCRIPTION_ENABLED", "true")
	orig := transcriptionJobTimeout
	transcriptionJobTimeout = 30 * time.Millisecond
	t.Cleanup(func() { transcriptionJobTimeout = orig })
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectExec(`UPDATE videos SET transcript_status = 'failed'`).
		WithArgs("video-1", 1, 2).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	processTranscription(context.Background(), &statusWriteDB{PgxPoolIface: mock}, hangingStorage{&mockStorage{}}, stubTranscriber{available: true},
		"video-1", "recordings/u/v.webm", 1, 0, "user-1", "share", "auto", false, 2)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("failure not recorded at the deadline: %v", err)
	}
}

// Just after a start, a missing-job answer is the service catching up, not
// the job being gone: the job is still waited for and used.
func TestAWSTranscriberWaitsOutReadAfterCreate(t *testing.T) {
	tr, api, storage, audio := newFakeAWSTranscriber(t, transcribeFixture)
	api.missingAfterStart = 3

	if _, err := tr.Transcribe(context.Background(), audio, "auto"); err != nil {
		t.Fatalf("err = %v, want the transcript once the job is visible", err)
	}
	assertNothingStaged(t, storage, api)
}

// After the grace, a missing job is gone and the wait ends for the fallback.
func TestAWSTranscriberMissingJobAfterTheGrace(t *testing.T) {
	prev := readAfterCreateGrace
	readAfterCreateGrace = 0
	t.Cleanup(func() { readAfterCreateGrace = prev })
	tr, api, storage, audio := newFakeAWSTranscriber(t, transcribeFixture)
	api.missingAfterStart = 1 << 30

	if _, err := tr.Transcribe(context.Background(), audio, "auto"); !errors.Is(err, errJobNotFound) {
		t.Fatalf("err = %v, want the missing job", err)
	}
	assertNothingStaged(t, storage, api)
}

// An abandoned job the settle cannot see yet, just after its start, is still
// waited for, so its output is deleted when it lands.
func TestAWSTranscriberSettleWaitsOutReadAfterCreate(t *testing.T) {
	tr, api, storage, audio := newFakeAWSTranscriber(t, transcribeFixture)
	api.readyAt = time.Now().Add(150 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := tr.Transcribe(ctx, audio, "auto"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	api.mu.Lock()
	api.missingAfterStart = 5 // the settle's first polls see no job
	api.mu.Unlock()

	assertNothingStaged(t, storage, api)
	if time.Now().Before(api.readyAt) {
		t.Fatal("the settle gave up before the job wrote its output")
	}
}

// When the start's probe proves no job exists, there is nothing to settle or
// delete, and a denied call is named as configuration, once.
func TestAWSTranscriberNoJobNothingToSettle(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	tr, api, storage, audio := newFakeAWSTranscriber(t, transcribeFixture)
	api.startErrs, api.startErr = 99, errAccessDenied

	if _, err := tr.Transcribe(context.Background(), audio, "auto"); !errors.Is(err, errAccessDenied) {
		t.Fatalf("err = %v", err)
	}
	api.mu.Lock()
	deleted, gets := len(api.deleted), api.getCalls
	api.mu.Unlock()
	if deleted != 0 || gets != 0 {
		t.Errorf("deleted %d jobs and polled %d times for a job that never existed", deleted, gets)
	}
	assertNothingStaged(t, storage, &fakeTranscribe{})
	if n := strings.Count(logs.String(), "check the task role"); n != 1 {
		t.Errorf("access denied logged %d times, want once:\n%s", n, logs.String())
	}
}

// slowWhisper stands in for local Whisper: it answers after delay, or ends
// with its context.
type slowWhisper struct {
	delay     time.Duration
	segments  []TranscriptSegment
	err       error
	mu        sync.Mutex
	cancelled bool
}

func (s *slowWhisper) Name() string    { return "local-whisper" }
func (s *slowWhisper) Available() bool { return true }
func (s *slowWhisper) Transcribe(ctx context.Context, _, _ string) ([]TranscriptSegment, error) {
	select {
	case <-time.After(s.delay):
		return s.segments, s.err
	case <-ctx.Done():
		s.mu.Lock()
		s.cancelled = true
		s.mu.Unlock()
		return nil, ctx.Err()
	}
}

// abandonedThenCompletes makes the job's first poll fail for good, so the
// provider abandons it at once, and the job completes readyIn later.
func abandonedThenCompletes(t *testing.T, readyIn time.Duration) (*fallbackTranscriber, *fakeTranscribe, *memStorage, string, *slowWhisper) {
	t.Helper()
	tr, api, storage, audio := newFakeAWSTranscriber(t, transcribeFixture)
	api.getErrs, api.getErr = 1, errAccessDenied
	api.readyAt = time.Now().Add(readyIn)
	whisper := &slowWhisper{}
	return &fallbackTranscriber{primary: tr, fallback: whisper}, api, storage, audio, whisper
}

// A job abandoned while running that completes before Whisper finishes wins:
// its transcript is used and the Whisper run is cancelled.
func TestFallbackPrefersALateTranscribeResult(t *testing.T) {
	f, api, storage, audio, whisper := abandonedThenCompletes(t, 30*time.Millisecond)
	whisper.delay = 10 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	got, err := f.Transcribe(ctx, audio, "auto")
	if err != nil || len(got) != 2 || got[0].Text != "Hi, Sam." {
		t.Fatalf("got %+v, %v, want Transcribe's late transcript", got, err)
	}
	whisper.mu.Lock()
	cancelled := whisper.cancelled
	whisper.mu.Unlock()
	if !cancelled {
		t.Error("the losing Whisper run was not cancelled")
	}
	assertNothingStaged(t, storage, api)
}

// Whisper finishing first is used, and the late job's output is still deleted.
func TestFallbackUsesWhisperWhenItFinishesFirst(t *testing.T) {
	f, api, storage, audio, whisper := abandonedThenCompletes(t, 200*time.Millisecond)
	whisper.segments = []TranscriptSegment{{Start: 0, End: 1, Text: "from whisper"}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	got, err := f.Transcribe(ctx, audio, "auto")
	if err != nil || len(got) != 1 || got[0].Text != "from whisper" {
		t.Fatalf("got %+v, %v, want Whisper's transcript", got, err)
	}
	assertNothingStaged(t, storage, api)
}

// A Whisper run that fails still leaves the abandoned job's late transcript.
func TestFallbackWaitsForALateResultAfterWhisperFails(t *testing.T) {
	f, api, storage, audio, whisper := abandonedThenCompletes(t, 100*time.Millisecond)
	whisper.err = errors.New("whisper-cli: killed")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	got, err := f.Transcribe(ctx, audio, "auto")
	if err != nil || len(got) != 2 {
		t.Fatalf("got %+v, %v, want Transcribe's late transcript", got, err)
	}
	assertNothingStaged(t, storage, api)
}

// Whisper starts at once while the task encodes: it yields the CPU at nice
// 19, so waiting would only spend its reserve.
func TestFallbackWhisperStartsWhileEncoding(t *testing.T) {
	holdEncoderSlots(t, 1)
	primary := &stubTranscribeResult{name: "aws-transcribe", err: errSyntheticAWS}
	fallback := &stubTranscribeResult{name: "local-whisper", segments: []TranscriptSegment{{Text: "x"}}}
	start := time.Now()
	if _, err := (&fallbackTranscriber{primary, fallback}).Transcribe(context.Background(), "audio.wav", "auto"); err != nil {
		t.Fatal(err)
	}
	if fallback.calledAt.Sub(start) > time.Second {
		t.Errorf("Whisper waited %v for the encode", fallback.calledAt.Sub(start))
	}
}
