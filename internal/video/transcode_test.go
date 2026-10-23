package video

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v5"
	"github.com/sendrec/sendrec/internal/database"
)

func TestTranscodeWebMAsync_DownloadError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	s := &mockStorage{downloadToFileErr: fmt.Errorf("s3 down")}

	TranscodeWebMAsync(context.Background(), mock, s, "video-123", "recordings/user/video.webm", "")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestTranscodeWebMAsync_FFmpegFails(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	s := &mockStorage{}

	TranscodeWebMAsync(context.Background(), mock, s, "video-123", "recordings/user/video.webm", "")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestIsPermanentFFmpegError(t *testing.T) {
	if isPermanentFFmpegError(nil) {
		t.Error("expected nil error to be non-permanent")
	}
	// The failure seen in production: browser upload stored a webm without a
	// readable header, so every retry produced the same result.
	corrupt := fmt.Errorf("ffmpeg transcode: exit status 183: [matroska,webm] EBML header parsing failed\nError opening input: Invalid data found when processing input")
	if !isPermanentFFmpegError(corrupt) {
		t.Error("expected corrupt webm error to be permanent")
	}
	if isPermanentFFmpegError(fmt.Errorf("ffmpeg transcode: exit status 1: No space left on device")) {
		t.Error("expected transient error to be non-permanent")
	}
}

func TestBuildTranscodeArgs(t *testing.T) {
	t.Run("without audio filter", func(t *testing.T) {
		args := buildTranscodeArgs("input.webm", "output.mp4", "", outputFrame{})
		if slices.Contains(args, "-af") {
			t.Error("expected no -af flag when audioFilter is empty")
		}
		if !slices.Contains(args, "-c:a") {
			t.Error("expected -c:a flag")
		}
	})

	t.Run("with audio filter", func(t *testing.T) {
		args := buildTranscodeArgs("input.webm", "output.mp4", "arnndn=m=/app/models/std.rnnn", outputFrame{})
		afIdx := slices.Index(args, "-af")
		if afIdx == -1 {
			t.Fatal("expected -af flag")
		}
		if args[afIdx+1] != "arnndn=m=/app/models/std.rnnn" {
			t.Errorf("expected filter value, got %q", args[afIdx+1])
		}
		// -af should come before -c:a
		caIdx := slices.Index(args, "-c:a")
		if afIdx >= caIdx {
			t.Error("expected -af before -c:a")
		}
	})
}

func TestBuildNormalizeArgs(t *testing.T) {
	t.Run("without audio filter", func(t *testing.T) {
		args := buildNormalizeArgs("input.mp4", "output.mp4", "", outputFrame{})
		if slices.Contains(args, "-af") {
			t.Error("expected no -af flag when audioFilter is empty")
		}
	})

	t.Run("with audio filter", func(t *testing.T) {
		args := buildNormalizeArgs("input.mp4", "output.mp4", "afftdn=nr=12:nf=-50", outputFrame{})
		afIdx := slices.Index(args, "-af")
		if afIdx == -1 {
			t.Fatal("expected -af flag")
		}
		if args[afIdx+1] != "afftdn=nr=12:nf=-50" {
			t.Errorf("expected filter value, got %q", args[afIdx+1])
		}
	})

	t.Run("video filter keeps one frame across a resize", func(t *testing.T) {
		for name, args := range map[string][]string{
			"normalize": buildNormalizeArgs("input.mp4", "output.mp4", "", outputFrame{1500, 1018}),
			"transcode": buildTranscodeArgs("input.webm", "output.mp4", "", outputFrame{1500, 1018}),
			"trim":      buildTrimArgs("input.mp4", "output.mp4", "video/mp4", 0, 1, outputFrame{1500, 1018}),
		} {
			vfIdx := slices.Index(args, "-vf")
			if vfIdx == -1 {
				t.Fatalf("%s: expected -vf flag", name)
			}
			want := (outputFrame{1500, 1018}).filter()
			if args[vfIdx+1] != want {
				t.Errorf("%s: -vf = %q, want the fixed frame filter", name, args[vfIdx+1])
			}
		}
	})
}

func TestRecordTranscodeFailure_TransientIncrementsAttempts(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery(`UPDATE videos`).
		WithArgs("video-1", "no space left on device", false, maxTranscodeAttempts, pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"transcode_attempts"}).AddRow(2))

	recordTranscodeFailure(context.Background(), mock, "video-1", "k", fmt.Errorf("no space left on device"))

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestRecordTranscodeFailure_PermanentConsumesBudget(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	cause := fmt.Errorf("ffmpeg transcode: exit status 183: EBML header parsing failed")

	// permanent = true, so the statement sets attempts straight to the cap
	// instead of incrementing.
	mock.ExpectQuery(`UPDATE videos`).
		WithArgs("video-1", cause.Error(), true, maxTranscodeAttempts, pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"transcode_attempts"}).AddRow(maxTranscodeAttempts))
	// Giving up is the one failure the owner has to hear about. Audit 3.8.
	mock.ExpectExec(`UPDATE videos SET processing_error = \$2`).
		WithArgs("video-1", conversionFailedMessage, pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	recordTranscodeFailure(context.Background(), mock, "video-1", "k", cause)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestRecordTranscodeFailure_TruncatesLongMessage(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	// ffmpeg dumps its whole log into the error, which can be far larger than
	// anything worth keeping in a column.
	long := strings.Repeat("x", 5000)

	mock.ExpectQuery(`UPDATE videos`).
		WithArgs("video-1", strings.Repeat("x", 2000), false, maxTranscodeAttempts, pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"transcode_attempts"}).AddRow(1))

	recordTranscodeFailure(context.Background(), mock, "video-1", "k", fmt.Errorf("%s", long))

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestRecordTranscodeFailure_TruncationKeepsValidUTF8(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	// ffmpeg quotes the input filename, so the log can carry multi-byte runes.
	// 2000 is not a multiple of 3, so the cut lands inside a rune; Postgres
	// rejects invalid UTF-8 and the attempt counter would never advance.
	long := strings.Repeat("日", 2000)

	mock.ExpectQuery(`UPDATE videos`).
		WithArgs("video-1", strings.Repeat("日", 666), false, maxTranscodeAttempts, pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"transcode_attempts"}).AddRow(1))

	recordTranscodeFailure(context.Background(), mock, "video-1", "k", fmt.Errorf("%s", long))

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestRecordTranscodeFailure_HandlesDBError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery(`UPDATE videos`).
		WithArgs("video-1", "boom", false, maxTranscodeAttempts, pgxmock.AnyArg()).
		WillReturnError(errors.New("connection refused"))

	// Should not panic — the caller has already given up on this attempt.
	recordTranscodeFailure(context.Background(), mock, "video-1", "k", fmt.Errorf("boom"))

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestTranscodeExistingWebM_SkipsExhaustedVideos(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery(`transcode_attempts < \$1`).
		WithArgs(maxTranscodeAttempts).
		WillReturnRows(pgxmock.NewRows([]string{"id", "file_key", "duration"}))

	transcodeExistingWebM(context.Background(), mock, &mockStorage{})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestNormalizeExistingVideos_SkipsExhaustedVideos(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery(`transcode_attempts < \$1`).
		WithArgs(maxTranscodeAttempts).
		WillReturnRows(pgxmock.NewRows([]string{"id", "file_key", "duration"}))

	normalizeExistingVideos(context.Background(), mock, &mockStorage{})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// A job enqueued from the finalize handler reaches TranscodeWebMAsync without
// passing through the worker's transcode_attempts filter.
func TestTranscodeWebMAsync_StopsWhenBudgetExhausted(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	s := &mockStorage{}

	mock.ExpectQuery(`SELECT content_type, transcode_attempts, duration FROM videos`).
		WithArgs("video-1").
		WillReturnRows(pgxmock.NewRows([]string{"content_type", "transcode_attempts", "duration"}).
			AddRow("video/webm", maxTranscodeAttempts, 120))

	TranscodeWebMAsync(context.Background(), mock, s, "video-1", "recordings/user/video.webm", "")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
	if s.downloadCalls() != 0 {
		t.Errorf("expected no download, got %d", s.downloadCalls())
	}
}

func TestTranscodeWebMAsync_ProceedsBelowBudget(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	s := &mockStorage{downloadToFileErr: fmt.Errorf("s3 down")}

	mock.ExpectQuery(`SELECT content_type, transcode_attempts, duration FROM videos`).
		WithArgs("video-1").
		WillReturnRows(pgxmock.NewRows([]string{"content_type", "transcode_attempts", "duration"}).
			AddRow("video/webm", maxTranscodeAttempts-1, 120))

	mock.ExpectQuery(`UPDATE videos`).
		WithArgs("video-1", "s3 down", false, maxTranscodeAttempts, pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"transcode_attempts"}).AddRow(maxTranscodeAttempts))
	// Giving up is the one failure the owner has to hear about. Audit 3.8.
	mock.ExpectExec(`UPDATE videos SET processing_error = \$2`).
		WithArgs("video-1", conversionFailedMessage, pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	TranscodeWebMAsync(context.Background(), mock, s, "video-1", "recordings/user/video.webm", "")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
	if s.downloadCalls() != 1 {
		t.Errorf("expected 1 download attempt, got %d", s.downloadCalls())
	}
}

func TestNormalizeVideoAsync_StopsWhenBudgetExhausted(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	s := &mockStorage{}

	mock.ExpectQuery(`SELECT ios_normalized, transcode_attempts, duration FROM videos`).
		WithArgs("video-1").
		WillReturnRows(pgxmock.NewRows([]string{"ios_normalized", "transcode_attempts", "duration"}).
			AddRow(false, maxTranscodeAttempts, 120))

	NormalizeVideoAsync(context.Background(), mock, s, "video-1", "recordings/user/video.mp4", "")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
	if s.downloadCalls() != 0 {
		t.Errorf("expected no download, got %d", s.downloadCalls())
	}
}

// stubFFmpeg replaces the ffmpeg call with one that just writes the output
// file, so a test can reach the upload and db-update steps that follow.
func stubFFmpeg(t *testing.T, target *func(context.Context, string, string, string) error) {
	t.Helper()
	original := *target
	*target = func(_ context.Context, _, outputPath, _ string) error {
		return os.WriteFile(outputPath, []byte("fake mp4"), 0o600)
	}
	t.Cleanup(func() { *target = original })
}

// An upload failure leaves the video exactly as the worker query found it, so
// without an attempt bump the worker re-downloads and re-runs ffmpeg forever.
func TestTranscodeWebMAsync_UploadFailureConsumesBudget(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	stubFFmpeg(t, &transcodeToMP4)
	s := &mockStorage{uploadFileErr: fmt.Errorf("s3 unavailable")}

	mock.ExpectQuery(`SELECT content_type, transcode_attempts, duration FROM videos`).
		WithArgs("video-1").
		WillReturnRows(pgxmock.NewRows([]string{"content_type", "transcode_attempts", "duration"}).
			AddRow("video/webm", 0, 120))
	mock.ExpectExec(`INSERT INTO retired_objects`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`INSERT INTO retired_objects \(key, delete_after\) VALUES \(\$1, now\(\)\)`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery(`UPDATE videos`).
		WithArgs("video-1", "s3 unavailable", false, maxTranscodeAttempts, pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"transcode_attempts"}).AddRow(1))

	TranscodeWebMAsync(context.Background(), mock, s, "video-1", "recordings/user/video.webm", "")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// The transcoded object is already uploaded by this point, so an unbounded
// retry also leaves a duplicate .mp4 behind on every pass.
func TestTranscodeWebMAsync_DBUpdateFailureConsumesBudget(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	stubFFmpeg(t, &transcodeToMP4)
	s := &mockStorage{}

	mock.ExpectQuery(`SELECT content_type, transcode_attempts, duration FROM videos`).
		WithArgs("video-1").
		WillReturnRows(pgxmock.NewRows([]string{"content_type", "transcode_attempts", "duration"}).
			AddRow("video/webm", 0, 120))
	mock.ExpectExec(`INSERT INTO retired_objects`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery(`WITH held AS`).
		WithArgs("video-1", "recordings/user/video.webm", pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("deadlock detected"))
	mock.ExpectQuery(`UPDATE videos`).
		WithArgs("video-1", "deadlock detected", false, maxTranscodeAttempts, pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"transcode_attempts"}).AddRow(1))

	TranscodeWebMAsync(context.Background(), mock, s, "video-1", "recordings/user/video.webm", "")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestNormalizeVideoAsync_UploadFailureConsumesBudget(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	stubFFmpeg(t, &transcodeToIOSCompatible)
	// Report a non-h264 codec so needsNormalization() is true and the re-encode
	// path runs instead of the early "already compatible" exit.
	originalProbe := probeVideoProperties
	probeVideoProperties = func(context.Context, string) (videoProperties, error) {
		return videoProperties{CodecName: "vp9", Width: 1280, Height: 720}, nil
	}
	t.Cleanup(func() { probeVideoProperties = originalProbe })

	s := &mockStorage{uploadFileErr: fmt.Errorf("s3 unavailable")}

	mock.ExpectQuery(`SELECT ios_normalized, transcode_attempts, duration FROM videos`).
		WithArgs("video-1").
		WillReturnRows(pgxmock.NewRows([]string{"ios_normalized", "transcode_attempts", "duration"}).
			AddRow(false, 0, 120))
	mock.ExpectExec(`INSERT INTO retired_objects`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`INSERT INTO retired_objects \(key, delete_after\) VALUES \(\$1, now\(\)\)`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery(`UPDATE videos`).
		WithArgs("video-1", "s3 unavailable", false, maxTranscodeAttempts, pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"transcode_attempts"}).AddRow(1))

	NormalizeVideoAsync(context.Background(), mock, s, "video-1", "recordings/user/video.mp4", "")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// ffmpeg output reaches this column verbatim, so a short message can still
// carry bytes Postgres refuses even when no truncation happens.
func TestRecordTranscodeFailure_SanitizesShortMessages(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	// Invalid UTF-8 and a NUL, well under the truncation threshold. NUL is
	// valid UTF-8, so ToValidUTF8 alone does not remove it.
	cause := fmt.Errorf("ffmpeg: %s", "bad\xffbyte\x00here")

	mock.ExpectQuery(`UPDATE videos`).
		WithArgs("video-1", "ffmpeg: badbytehere", false, maxTranscodeAttempts, pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"transcode_attempts"}).AddRow(1))

	recordTranscodeFailure(context.Background(), mock, "video-1", "k", cause)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestNormalizeVideoAsync_DBUpdateFailureConsumesBudget(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	stubFFmpeg(t, &transcodeToIOSCompatible)
	originalProbe := probeVideoProperties
	probeVideoProperties = func(context.Context, string) (videoProperties, error) {
		return videoProperties{CodecName: "vp9", Width: 1280, Height: 720}, nil
	}
	t.Cleanup(func() { probeVideoProperties = originalProbe })

	s := &mockStorage{}

	mock.ExpectQuery(`SELECT ios_normalized, transcode_attempts, duration FROM videos`).
		WithArgs("video-1").
		WillReturnRows(pgxmock.NewRows([]string{"ios_normalized", "transcode_attempts", "duration"}).
			AddRow(false, 0, 120))
	mock.ExpectExec(`INSERT INTO retired_objects`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery(`WITH held AS`).
		WithArgs("video-1", "recordings/user/video.mp4", pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("deadlock detected"))
	mock.ExpectQuery(`UPDATE videos`).
		WithArgs("video-1", "deadlock detected", false, maxTranscodeAttempts, pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"transcode_attempts"}).AddRow(1))

	NormalizeVideoAsync(context.Background(), mock, s, "video-1", "recordings/user/video.mp4", "")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// ctxDB fails on an expired context the way a real pgx pool does; pgxmock
// alone ignores the context.
type ctxDB struct{ database.DBTX }

type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

func (d ctxDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if err := ctx.Err(); err != nil {
		return errRow{err}
	}
	return d.DBTX.QueryRow(ctx, sql, args...)
}

func (d ctxDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if err := ctx.Err(); err != nil {
		return pgconn.CommandTag{}, err
	}
	return d.DBTX.Exec(ctx, sql, args...)
}

// The commonest failure is the job's own deadline. By then its context has
// expired, and writing the failure on it fails too, so the attempt never
// counted and the job retried forever on the only encoder slot.
func TestRecordTranscodeFailure_CountsAfterTheJobTimedOut(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery(`UPDATE videos`).
		WithArgs("video-1", pgxmock.AnyArg(), false, maxTranscodeAttempts, pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"transcode_attempts"}).AddRow(1))

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	recordTranscodeFailure(ctx, ctxDB{mock}, "video-1", "k", ctx.Err())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("the failure was not recorded after the timeout: %v", err)
	}
}

// A probe that fails tells us nothing about the file, so the video must not
// be recorded as compatible: that would take it out of every later pass while
// it may still fail on iPhones. It counts as an attempt and is retried within
// the budget. Audit BG-05.
func TestNormalizeVideoAsync_ProbeFailureIsAnAttemptNotAVerdict(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	originalProbe := probeVideoProperties
	probeVideoProperties = func(context.Context, string) (videoProperties, error) {
		return videoProperties{}, fmt.Errorf("ffprobe: exit status 1")
	}
	t.Cleanup(func() { probeVideoProperties = originalProbe })

	mock.ExpectQuery(`SELECT ios_normalized, transcode_attempts, duration FROM videos`).
		WithArgs("video-1").
		WillReturnRows(pgxmock.NewRows([]string{"ios_normalized", "transcode_attempts", "duration"}).
			AddRow(false, 0, 120))
	mock.ExpectQuery(`UPDATE videos`).
		WithArgs("video-1", "ffprobe: exit status 1", false, maxTranscodeAttempts, pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"transcode_attempts"}).AddRow(1))

	NormalizeVideoAsync(context.Background(), mock, &mockStorage{}, "video-1", "recordings/user/video.mp4", "")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// deadlineDB records the deadline of the context each job reads its row with.
type deadlineDB struct {
	database.DBTX
	mu        sync.Mutex
	deadlines []time.Time
}

func (db *deadlineDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	deadline, _ := ctx.Deadline()
	db.mu.Lock()
	db.deadlines = append(db.deadlines, deadline)
	db.mu.Unlock()
	return db.DBTX.QueryRow(ctx, sql, args...)
}

func (db *deadlineDB) recorded() []time.Time {
	db.mu.Lock()
	defer db.mu.Unlock()
	return append([]time.Time(nil), db.deadlines...)
}

// expectEncodeDeadline checks that one job ran with encodeTimeout(600): ten
// minutes plus twice ten minutes, so a bit under thirty from now.
func expectEncodeDeadline(t *testing.T, deadlines []time.Time) {
	t.Helper()
	if len(deadlines) != 1 {
		t.Fatalf("got %d job contexts, want one", len(deadlines))
	}
	if remaining := time.Until(deadlines[0]); remaining <= 29*time.Minute || remaining > 30*time.Minute {
		t.Errorf("job deadline is %v away, want just under 30 min for a 600 s recording", remaining.Round(time.Second))
	}
}

// Every Staff OS take is re-encoded once now, on a two-vCPU host with one
// encoder slot, so a flat ten minutes cut long takes off. The deadline grows
// with the recording the way the composite's does, from the worker and from
// the upload handler alike.
func TestEncodeDeadlinesGrowWithTheRecording(t *testing.T) {
	expectAlreadyDone := func(mock pgxmock.PgxPoolIface, job JobType) {
		// The row says the job is done, so it exits after this first read.
		switch job {
		case JobTypeTranscode:
			mock.ExpectQuery(`SELECT content_type, transcode_attempts, duration FROM videos`).
				WithArgs("video-1").
				WillReturnRows(pgxmock.NewRows([]string{"content_type", "transcode_attempts", "duration"}).
					AddRow("video/mp4", 0, 600))
		case JobTypeNormalize:
			mock.ExpectQuery(`SELECT ios_normalized, transcode_attempts, duration FROM videos`).
				WithArgs("video-1").
				WillReturnRows(pgxmock.NewRows([]string{"ios_normalized", "transcode_attempts", "duration"}).
					AddRow(true, 0, 600))
		}
	}

	for _, job := range []JobType{JobTypeTranscode, JobTypeNormalize} {
		t.Run(string(job)+" worker", func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			defer mock.Close()
			mock.ExpectQuery(`transcode_attempts < \$1`).
				WithArgs(maxTranscodeAttempts).
				WillReturnRows(pgxmock.NewRows([]string{"id", "file_key", "duration"}).AddRow("video-1", "recordings/user/video", 600))
			expectAlreadyDone(mock, job)

			db := &deadlineDB{DBTX: mock}
			if job == JobTypeTranscode {
				transcodeExistingWebM(context.Background(), db, &mockStorage{})
			} else {
				normalizeExistingVideos(context.Background(), db, &mockStorage{})
			}
			expectEncodeDeadline(t, db.recorded())
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet expectations: %v", err)
			}
		})

		t.Run(string(job)+" job", func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			defer mock.Close()
			expectAlreadyDone(mock, job)

			db := &deadlineDB{DBTX: mock}
			handler := NewHandler(db, &mockStorage{}, testBaseURL, 0, 0, 0, 0, testJWTSecret, false)
			handler.EnqueueJob(context.Background(), job, "video-1", map[string]any{
				"fileKey": "recordings/user/video", "audioFilter": "", "duration": 600,
			})
			// The job runs on its own goroutine.
			deadline := time.Now().Add(5 * time.Second)
			for len(db.recorded()) == 0 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			expectEncodeDeadline(t, db.recorded())
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet expectations: %v", err)
			}
		})
	}
}

// deadlineStorage records the deadline of the context an edit downloads its
// input with, then fails the download so the edit stops there.
type deadlineStorage struct {
	*mockStorage
	mu        sync.Mutex
	deadlines []time.Time
}

func (s *deadlineStorage) DownloadToFile(ctx context.Context, _ string, _ string) error {
	deadline, _ := ctx.Deadline()
	s.mu.Lock()
	s.deadlines = append(s.deadlines, deadline)
	s.mu.Unlock()
	return errors.New("download stopped by the test")
}

func (s *deadlineStorage) recorded() []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Time(nil), s.deadlines...)
}

// A trim or segment removal re-encodes the whole take and can wait for the
// one encoder slot behind a normalisation that now runs on every avc3 take,
// so a flat ten minutes failed edits of long recordings. They get the same
// deadline as the other encodes.
func TestEditDeadlinesGrowWithTheRecording(t *testing.T) {
	const videoID = "video-123"
	const fileKey = "recordings/user/video.mp4"
	edits := []struct {
		name  string
		route string
		body  string
		serve func(*Handler) http.HandlerFunc
	}{
		{"trim", "/api/videos/{id}/trim", `{"startSeconds": 5.0, "endSeconds": 30.0}`, func(h *Handler) http.HandlerFunc { return h.Trim }},
		{"remove-segments", "/api/videos/{id}/remove-segments", `{"segments": [{"start": 5.0, "end": 10.0}]}`, func(h *Handler) http.HandlerFunc { return h.RemoveSegments }},
	}
	for _, edit := range edits {
		t.Run(edit.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			defer mock.Close()
			mock.ExpectQuery(`SELECT duration, file_key, share_token, status, content_type, user_id FROM videos`).
				WithArgs(videoID, testUserID).
				WillReturnRows(pgxmock.NewRows([]string{"duration", "file_key", "share_token", "status", "content_type", "user_id"}).
					AddRow(600, fileKey, "abc123defghi", "ready", "video/mp4", testUserID))
			mock.ExpectQuery(`UPDATE videos SET status = 'processing'.* RETURNING media_version`).
				WithArgs(fileKey, videoID, testUserID).
				WillReturnRows(pgxmock.NewRows([]string{"media_version"}).AddRow(1))
			mock.ExpectExec(`UPDATE videos SET status = 'ready'`).
				WithArgs(videoID, editFailedMessage, fileKey).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))

			storage := &deadlineStorage{mockStorage: &mockStorage{}}
			handler := NewHandler(mock, storage, testBaseURL, 0, 0, 0, 0, testJWTSecret, false)
			r := chi.NewRouter()
			r.With(newAuthMiddleware()).Post(edit.route, edit.serve(handler))
			rec := httptest.NewRecorder()
			path := strings.Replace(edit.route, "{id}", videoID, 1)
			r.ServeHTTP(rec, authenticatedRequest(t, http.MethodPost, path, []byte(edit.body)))
			if rec.Code != http.StatusAccepted {
				t.Fatalf("got status %d: %s", rec.Code, rec.Body.String())
			}

			// The edit runs on its own goroutine and resets the row once the
			// download fails.
			deadline := time.Now().Add(5 * time.Second)
			for mock.ExpectationsWereMet() != nil && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
			expectEncodeDeadline(t, storage.recorded())
		})
	}
}
