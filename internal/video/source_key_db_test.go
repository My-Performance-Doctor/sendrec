package video

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// moveKey switches the video to another file the way an edit committing at
// that moment would.
func moveKey(t *testing.T, pool *pgxpool.Pool, videoID, key string) {
	t.Helper()
	mustExecDB(t, pool, `UPDATE videos SET file_key = $2, status = 'ready' WHERE id = $1`, videoID, key)
}

type sourceRow struct {
	warning  *string
	attempts int
	errText  *string
}

func readSourceRow(t *testing.T, pool *pgxpool.Pool, videoID string) sourceRow {
	t.Helper()
	var r sourceRow
	if err := pool.QueryRow(context.Background(),
		`SELECT capture_warning, transcode_attempts, transcode_error FROM videos WHERE id = $1`, videoID,
	).Scan(&r.warning, &r.attempts, &r.errText); err != nil {
		t.Fatal(err)
	}
	return r
}

func seedReadyVideo(t *testing.T, pool *pgxpool.Pool, key, contentType string) string {
	t.Helper()
	videoID := seedProcessingVideo(t, pool, key, contentType)
	mustExecDB(t, pool, `UPDATE videos SET status = 'ready', processing_started_at = NULL WHERE id = $1`, videoID)
	return videoID
}

// #328: a conversion that loses to an edit must not write the capture verdict
// it computed from the file the edit replaced.
func TestStaleConversionLeavesCaptureWarning(t *testing.T) {
	for _, job := range conversionJobs {
		t.Run(job.name, func(t *testing.T) {
			pool := accountDB(t)
			videoID := seedReadyVideo(t, pool, job.key, job.contentType)
			stubConversions(t, "converted", nil)
			probe := probeStreamDurations
			probeStreamDurations = func(context.Context, string) (streamDurations, error) {
				moveKey(t, pool, videoID, "recordings/u/tok.edited"+job.key[len(job.key)-4:])
				return streamDurations{Video: 10, Audio: 120, HasVideo: true}, nil
			}
			t.Cleanup(func() { probeStreamDurations = probe })

			job.run(context.Background(), pool, newMemStorage(map[string]string{job.key: "original"}), videoID, job.key)

			if got := readSourceRow(t, pool, videoID); got.warning != nil {
				t.Errorf("capture_warning = %q, written from a file the video no longer uses", *got.warning)
			}
		})
	}
}

// #328: the same conversion failing must not spend the edited file's budget.
func TestStaleConversionFailureIsNotCounted(t *testing.T) {
	for _, job := range conversionJobs {
		t.Run(job.name, func(t *testing.T) {
			pool := accountDB(t)
			videoID := seedReadyVideo(t, pool, job.key, job.contentType)
			stubConversions(t, "converted", nil)
			fail := func(context.Context, string, string, string) error {
				moveKey(t, pool, videoID, "recordings/u/tok.edited"+job.key[len(job.key)-4:])
				return errors.New("Invalid data found when processing input")
			}
			transcodeToMP4, transcodeToIOSCompatible = fail, fail

			job.run(context.Background(), pool, newMemStorage(map[string]string{job.key: "original"}), videoID, job.key)

			if got := readSourceRow(t, pool, videoID); got.attempts != 0 || got.errText != nil {
				t.Errorf("attempts = %d, error = %v; want the edited file's budget untouched", got.attempts, got.errText)
			}
		})
	}
}

// The counter still works for the file the job converted.
func TestConversionFailureIsCountedForItsSource(t *testing.T) {
	pool := accountDB(t)
	const key = "recordings/u/tok.webm"
	videoID := seedReadyVideo(t, pool, key, "video/webm")
	stubConversions(t, "converted", nil)
	transcodeToMP4 = func(context.Context, string, string, string) error { return errors.New("encoder crashed") }

	TranscodeWebMAsync(context.Background(), pool, newMemStorage(map[string]string{key: "original"}), videoID, key, "")

	if got := readSourceRow(t, pool, videoID); got.attempts != 1 {
		t.Errorf("attempts = %d, want 1", got.attempts)
	}
}
