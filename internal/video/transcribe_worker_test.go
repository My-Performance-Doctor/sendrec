package video

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v5"
	"github.com/sendrec/sendrec/internal/database"
)

type stubTranscriber struct {
	available bool
	segments  []TranscriptSegment
	err       error
}

func (s stubTranscriber) Name() string    { return "stub" }
func (s stubTranscriber) Available() bool { return s.available }
func (s stubTranscriber) Transcribe(ctx context.Context, audioPath, language string) ([]TranscriptSegment, error) {
	return s.segments, s.err
}

func TestEnqueueTranscription(t *testing.T) {
	t.Setenv("TRANSCRIPTION_ENABLED", "true")
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectExec(`UPDATE videos SET transcript_status = 'pending', transcript_generation = transcript_generation \+ 1, updated_at = now\(\)`).
		WithArgs("video-123").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	if err := EnqueueTranscription(context.Background(), mock, "video-123"); err != nil {
		t.Errorf("EnqueueTranscription failed: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestEnqueueTranscription_DisabledIsNoOp(t *testing.T) {
	t.Setenv("TRANSCRIPTION_ENABLED", "false")
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	// No DB expectations - call should short-circuit before touching DB.
	if err := EnqueueTranscription(context.Background(), mock, "video-123"); err != nil {
		t.Errorf("EnqueueTranscription failed: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestProcessNextTranscription_NoJobs(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	// Stuck job reset
	mock.ExpectExec(`UPDATE videos SET transcript_status = 'pending'`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	// Claim query returns no rows
	mock.ExpectQuery(`UPDATE videos SET transcript_status = 'processing'`).
		WillReturnRows(pgxmock.NewRows([]string{"id", "file_key", "media_version", "duration", "user_id", "share_token", "language", "transcript_generation"}))

	storage := &mockStorage{}
	processNextTranscription(context.Background(), mock, storage, stubTranscriber{available: true}, false)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestProcessNextTranscription_ResetsStuckJobs(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	// Stuck job reset — should match 1 row
	mock.ExpectExec(`UPDATE videos SET transcript_status = 'pending'`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	// Claim query returns no rows (the reset job will be picked up next tick)
	mock.ExpectQuery(`UPDATE videos SET transcript_status = 'processing'`).
		WillReturnRows(pgxmock.NewRows([]string{"id", "file_key", "media_version", "duration", "user_id", "share_token", "language", "transcript_generation"}))

	storage := &mockStorage{}
	processNextTranscription(context.Background(), mock, storage, stubTranscriber{available: true}, false)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// hangingStorage stands in for a job that never finishes on its own, like a
// hung whisper-cli: the download blocks until its context ends.
type hangingStorage struct{ *mockStorage }

func (hangingStorage) DownloadToFile(ctx context.Context, _, _ string) error {
	<-ctx.Done()
	return ctx.Err()
}

// One hung job used to hold the single transcription worker forever. Each job
// now has a deadline, and the failure is recorded once it passes. BG-07.
func TestProcessTranscription_HungJobFailsAtItsDeadline(t *testing.T) {
	t.Setenv("TRANSCRIPTION_ENABLED", "true")
	orig := transcriptionJobTimeout
	transcriptionJobTimeout = 50 * time.Millisecond
	t.Cleanup(func() { transcriptionJobTimeout = orig })

	db := &recoveryDB{}
	done := make(chan struct{})
	go func() {
		processTranscription(context.Background(), db, hangingStorage{&mockStorage{}}, stubTranscriber{available: true},
			"video-123", "recordings/u/v.webm", 0, 0, "u", "tok", "auto", false)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("transcription job never gave up")
	}
	if !db.updated {
		t.Error("want the timed-out job marked failed")
	}
}

// The stale-job reset must not requeue a job that is still inside its
// deadline, or a second worker would start it again.
func TestProcessNextTranscription_ResetWaitsOutTheDeadline(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectExec(`transcript_started_at < now\(\) - make_interval\(secs => \$1::float8 \+ LEAST\(GREATEST\(\$2::bigint \* duration, 0\), \$3::bigint\)\)`).
		WithArgs((transcriptionJobTimeout + time.Minute).Seconds(), transcriptionSecondsPerSecond, transcriptionExtraCapSeconds).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectQuery(`UPDATE videos SET transcript_status = 'processing'`).
		WillReturnRows(pgxmock.NewRows([]string{"id", "file_key", "media_version", "duration", "user_id", "share_token", "language", "transcript_generation"}))

	processNextTranscription(context.Background(), mock, &mockStorage{}, stubTranscriber{available: true}, false)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// A fixed 30 minutes failed 10 minute recordings, and without a transcript MPD
// never creates the patient's report. The deadline grows with the recording.
func TestTranscriptionTimeout(t *testing.T) {
	for _, tc := range []struct {
		duration int
		want     time.Duration
	}{
		{0, 30 * time.Minute},
		{-5, 30 * time.Minute},
		{60, 34 * time.Minute},
		{10 * 60, 70 * time.Minute},
		{30 * 60, 150 * time.Minute},
		{24 * 60 * 60, 30*time.Minute + 4*time.Hour},
	} {
		if got := transcriptionTimeout(tc.duration); got != tc.want {
			t.Errorf("transcriptionTimeout(%d) = %v, want %v", tc.duration, got, tc.want)
		}
	}
}

// Local Whisper yields the CPU to encodes, so a job claimed beside one would
// spend its deadline waiting: while this task encodes, the job stays pending
// for an idle task. A provider that runs off the task claims it at once.
func TestProcessNextTranscription_OnlyLocalWhisperWaitsForEncodes(t *testing.T) {
	for _, tc := range []struct {
		name        string
		transcriber Transcriber
		claims      int
	}{
		{"local whisper", newLocalWhisper(), 0},
		{"off-task provider", stubTranscriber{available: true}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			holdEncoderSlots(t, 1)
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			defer mock.Close()

			// The stuck-job reset always runs.
			mock.ExpectExec(`UPDATE videos SET transcript_status = 'pending'`).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnResult(pgxmock.NewResult("UPDATE", 0))
			if tc.claims > 0 {
				mock.ExpectQuery(`UPDATE videos SET transcript_status = 'processing'`).WillReturnError(pgx.ErrNoRows)
			}
			db := &countingDB{DBTX: mock}

			processNextTranscription(context.Background(), db, &mockStorage{}, tc.transcriber, false)

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet expectations: %v", err)
			}
			if db.queryRows != tc.claims {
				t.Errorf("claims attempted = %d, want %d", db.queryRows, tc.claims)
			}
		})
	}
}

// countingDB counts QueryRow calls, which is how the worker claims a job.
type countingDB struct {
	database.DBTX
	queryRows int
}

func (c *countingDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	c.queryRows++
	return c.DBTX.QueryRow(ctx, sql, args...)
}

// holdEncoderSlots takes every slot of a fresh gate of the given size for the
// rest of the test, as running encodes would.
func holdEncoderSlots(t *testing.T, limit int) {
	t.Helper()
	prev := ffmpegEncoders.Load()
	slots := newEncoderSlots(limit)
	ffmpegEncoders.Store(slots)
	var releases []func()
	for range limit {
		release, err := slots.acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	t.Cleanup(func() {
		for _, release := range releases {
			release()
		}
		ffmpegEncoders.Store(prev)
	})
}

// A deploy or scale-in stops the task mid-job. The job goes straight back to
// the queue instead of waiting out a deadline that grows with the recording,
// and the stopping worker claims nothing more.
func TestRequeueInFlightTranscription(t *testing.T) {
	t.Cleanup(func() {
		transcriptionStopping.Store(false)
		inFlightTranscription.Store(nil)
	})
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	inFlightTranscription.Store(&transcriptionJob{videoID: "video-123", generation: 4})
	mock.ExpectExec(`UPDATE videos SET transcript_status = 'pending', transcript_generation = transcript_generation \+ 1.*transcript_status = 'processing' AND transcript_generation = \$2`).
		WithArgs("video-123", 4).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	RequeueInFlightTranscription(context.Background(), mock)
	// No reset and no claim once stopping.
	processNextTranscription(context.Background(), mock, &mockStorage{}, stubTranscriber{available: true}, false)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestRequeueInFlightTranscription_NothingRunning(t *testing.T) {
	t.Cleanup(func() { transcriptionStopping.Store(false) })
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	RequeueInFlightTranscription(context.Background(), mock)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// stopDuringClaim begins shutdown while the claim is in flight, after
// RequeueInFlightTranscription has already looked and found nothing running.
type stopDuringClaim struct{ pgxmock.PgxPoolIface }

func (s stopDuringClaim) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	transcriptionStopping.Store(true)
	return s.PgxPoolIface.QueryRow(ctx, sql, args...)
}

// A job claimed as the task begins to stop goes straight back to the queue,
// instead of waiting out its deadline in the stuck-job reset.
func TestProcessNextTranscription_RequeuesAJobClaimedDuringShutdown(t *testing.T) {
	holdEncoderSlots(t, 0) // a fresh, idle gate
	t.Cleanup(func() {
		transcriptionStopping.Store(false)
		inFlightTranscription.Store(nil)
	})
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectExec(`UPDATE videos SET transcript_status = 'pending'`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectQuery(`UPDATE videos SET transcript_status = 'processing'`).
		WillReturnRows(pgxmock.NewRows([]string{"id", "file_key", "media_version", "duration", "user_id", "share_token", "language", "transcript_generation"}).
			AddRow("video-123", "recordings/v.webm", 1, 600, "user-1", "share", "auto", 3))
	mock.ExpectExec(`UPDATE videos SET transcript_status = 'pending', transcript_generation = transcript_generation \+ 1.*transcript_status = 'processing' AND transcript_generation = \$2`).
		WithArgs("video-123", 3).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	processNextTranscription(context.Background(), stopDuringClaim{mock}, &mockStorage{}, stubTranscriber{available: true}, false)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
	if inFlightTranscription.Load() != nil {
		t.Error("the requeued job is still marked as running")
	}
}
