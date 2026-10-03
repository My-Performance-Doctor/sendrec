package video

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func deleteVideo(t *testing.T, pool *pgxpool.Pool, videoID string) {
	t.Helper()
	mustExecDB(t, pool, `UPDATE videos SET status = 'deleted' WHERE id = $1`, videoID)
}

func readStatus(t *testing.T, pool *pgxpool.Pool, videoID string) (status string, thumbnailKey, transcriptKey *string, transcriptStatus *string) {
	t.Helper()
	if err := pool.QueryRow(context.Background(),
		`SELECT status, thumbnail_key, transcript_key, transcript_status FROM videos WHERE id = $1`, videoID,
	).Scan(&status, &thumbnailKey, &transcriptKey, &transcriptStatus); err != nil {
		t.Fatal(err)
	}
	return
}

// assertSweptClean runs the cleanup pass and checks that only want is left.
func assertSweptClean(t *testing.T, pool *pgxpool.Pool, s *memStorage, want ...string) {
	t.Helper()
	mustExecDB(t, pool, `UPDATE retired_objects SET delete_after = now() - INTERVAL '1 second' WHERE delete_after <= now() + INTERVAL '1 hour'`)
	DeleteRetiredObjects(context.Background(), pool, s)
	left := s.snapshot()
	for _, key := range want {
		delete(left, key)
	}
	if len(left) != 0 {
		t.Errorf("objects left after the cleanup pass: %v", left)
	}
}

func stubComposite(t *testing.T, during func()) {
	t.Helper()
	probe, overlay := probeVideoInfo, compositeOverlay
	probeVideoInfo = func(context.Context, string) (int, string, error) { return 100, "", nil }
	compositeOverlay = func(_ context.Context, _, _, out string) (string, error) {
		if during != nil {
			during()
		}
		return "", os.WriteFile(out, []byte("composited"), 0o600)
	}
	t.Cleanup(func() { probeVideoInfo, compositeOverlay = probe, overlay })
}

func stubFrame(t *testing.T, during func()) {
	t.Helper()
	frame := extractFrameAt
	extractFrameAt = func(_ context.Context, _, out string, _ int) error {
		if during != nil {
			during()
		}
		return os.WriteFile(out, []byte("jpeg"), 0o600)
	}
	t.Cleanup(func() { extractFrameAt = frame })
}

// #325: a composite still encoding when its video is deleted must neither
// publish it again nor leave its output behind.
func TestCompositeAfterDeletion(t *testing.T) {
	for _, contentType := range []string{"video/webm", "video/mp4"} {
		t.Run(contentType, func(t *testing.T) {
			pool := accountDB(t)
			ext := extensionForContentType(contentType)
			screen, webcam := "recordings/u/tok"+ext, "recordings/u/tok_webcam"+ext
			videoID := seedProcessingVideo(t, pool, screen, contentType)
			mustExecDB(t, pool, `UPDATE videos SET webcam_key = $2 WHERE id = $1`, videoID, webcam)
			stubFrame(t, nil)
			stubComposite(t, func() { deleteVideo(t, pool, videoID) })
			s := newMemStorage(map[string]string{screen: "screen", webcam: "webcam"})

			CompositeWithWebcam(context.Background(), pool, s, videoID, screen, webcam, "recordings/u/tok.jpg", contentType)

			if status, _, _, _ := readStatus(t, pool, videoID); status != "deleted" {
				t.Errorf("status = %q, want the deletion to stand", status)
			}
			// The purge owns the screen and webcam; nothing the job wrote may stay.
			assertSweptClean(t, pool, s, screen, webcam)
			if s.snapshot()[screen] != "screen" {
				t.Errorf("the composite overwrote the deleted video's screen recording")
			}
		})
	}
}

// #325: a composite that falls back after deletion must not publish the
// screen or generate a thumbnail for it.
func TestCompositeFallbackAfterDeletion(t *testing.T) {
	pool := accountDB(t)
	const screen, webcam = "recordings/u/tok.webm", "recordings/u/tok_webcam.webm"
	videoID := seedProcessingVideo(t, pool, screen, "video/webm")
	mustExecDB(t, pool, `UPDATE videos SET webcam_key = $2 WHERE id = $1`, videoID, webcam)
	stubFrame(t, nil)
	probe := probeVideoInfo
	probeVideoInfo = func(context.Context, string) (int, string, error) {
		deleteVideo(t, pool, videoID)
		return 0, "", nil // no frames: fall back
	}
	t.Cleanup(func() { probeVideoInfo = probe })
	s := newMemStorage(map[string]string{screen: "screen", webcam: "webcam"})

	CompositeWithWebcam(context.Background(), pool, s, videoID, screen, webcam, "recordings/u/tok.jpg", "video/webm")

	if status, thumb, _, _ := readStatus(t, pool, videoID); status != "deleted" || thumb != nil {
		t.Errorf("status = %q, thumbnail = %v; want the deletion to stand", status, thumb)
	}
	assertSweptClean(t, pool, s, screen, webcam)
}

// #325: a thumbnail made while the video is being deleted is not published,
// and its upload is reclaimed even though the deleted row may name the key.
func TestThumbnailAfterDeletion(t *testing.T) {
	pool := accountDB(t)
	const key, thumb = "recordings/u/tok.webm", "recordings/u/tok.jpg"
	videoID := seedReadyVideo(t, pool, key, "video/webm")
	mustExecDB(t, pool, `UPDATE videos SET thumbnail_key = $2 WHERE id = $1`, videoID, thumb)
	stubFrame(t, func() { deleteVideo(t, pool, videoID) })
	s := newMemStorage(map[string]string{key: "video"}) // purged thumbnail already gone

	GenerateThumbnail(context.Background(), pool, s, videoID, key, thumb)

	assertSweptClean(t, pool, s, key)
}

type hookTranscriber struct {
	stubTranscriber
	during func()
}

func (h hookTranscriber) Transcribe(ctx context.Context, audioPath, language string) ([]TranscriptSegment, error) {
	h.during()
	return h.stubTranscriber.Transcribe(ctx, audioPath, language)
}

// #325: a transcription that finishes after its video is deleted publishes
// nothing and leaves no transcript object behind.
func TestTranscriptionAfterDeletion(t *testing.T) {
	t.Setenv("TRANSCRIPTION_ENABLED", "true")
	pool := accountDB(t)
	const key = "recordings/u/tok.webm"
	videoID := seedReadyVideo(t, pool, key, "video/webm")
	audio := extractAudioAt
	extractAudioAt = func(_ context.Context, _, out string) error { return os.WriteFile(out, []byte("wav"), 0o600) }
	t.Cleanup(func() { extractAudioAt = audio })
	tr := hookTranscriber{
		stubTranscriber{available: true, segments: []TranscriptSegment{{Start: 0, End: 1, Text: "hello"}}},
		func() { deleteVideo(t, pool, videoID) },
	}
	s := newMemStorage(map[string]string{key: "video"})

	processTranscription(context.Background(), pool, s, tr, videoID, key, "u", "tok", "auto", false)

	if _, _, transcript, status := readStatus(t, pool, videoID); transcript != nil || (status != nil && *status == "ready") {
		t.Errorf("transcript_key = %v, status = %v; want nothing published", transcript, status)
	}
	assertSweptClean(t, pool, s, key)
}

// Keys named only by deleted rows are garbage: the purge has dealt with the
// row, so a late recreation of one of them must still be swept.
func TestDeleteRetiredObjectsIgnoresDeletedRows(t *testing.T) {
	pool := accountDB(t)
	videoID := seedReadyVideo(t, pool, "recordings/u/gone.webm", "video/webm")
	deleteVideo(t, pool, videoID)
	mustExecDB(t, pool, `INSERT INTO retired_objects (key, delete_after) VALUES ('recordings/u/gone.webm', now() - INTERVAL '1 minute')`)
	s := newMemStorage(map[string]string{"recordings/u/gone.webm": "late upload"})

	DeleteRetiredObjects(context.Background(), pool, s)

	if _, ok := s.snapshot()["recordings/u/gone.webm"]; ok {
		t.Error("a key named only by a deleted video survived the sweep")
	}
}

// A publication that matches no video must leave the upload's attempt record
// alone, so the upload stays tracked even if the job dies before discarding
// it.
func TestRejectedPublicationKeepsTheAttemptRecord(t *testing.T) {
	pool := accountDB(t)
	ctx := context.Background()
	const key = "recordings/u/tok.webm"
	videoID := seedProcessingVideo(t, pool, key, "video/webm")
	deleteVideo(t, pool, videoID)
	for _, k := range []string{"recordings/u/tok.n1.webm", "recordings/u/tok.t1.jpg"} {
		if err := recordReplacementAttempt(ctx, pool, k); err != nil {
			t.Fatal(err)
		}
	}

	switched, err := switchFileKey(ctx, pool,
		`UPDATE videos SET file_key = $3 WHERE id = $1 AND file_key = $2 AND status = 'processing'`,
		videoID, key, "recordings/u/tok.n1.webm")
	if err != nil || switched {
		t.Fatalf("switch on a deleted video = %t, %v", switched, err)
	}
	published, err := publishUpload(ctx, pool,
		`UPDATE videos SET thumbnail_key = $2 WHERE id = $1 AND status != 'deleted'`,
		videoID, "recordings/u/tok.t1.jpg")
	if err != nil || published {
		t.Fatalf("publish on a deleted video = %t, %v", published, err)
	}

	r := retired(t, pool)
	for _, k := range []string{"recordings/u/tok.n1.webm", "recordings/u/tok.t1.jpg"} {
		if r[k] < 23*time.Hour {
			t.Errorf("%s: attempt record due in %v (tracked %t), want it untouched", k, r[k], r[k] != 0)
		}
	}
}

// cancelOnDelete is a process that dies inside the storage delete.
type cancelOnDelete struct {
	*memStorage
	cancel context.CancelFunc
}

func (c cancelOnDelete) DeleteObject(context.Context, string) error {
	c.cancel()
	return context.Canceled
}

// The sweep's claim on a record lasts until the object is gone: a crash
// between the two must leave the record for the next run.
func TestSweepCrashKeepsTheRecord(t *testing.T) {
	pool := accountDB(t)
	mustExecDB(t, pool, `INSERT INTO retired_objects (key, delete_after) VALUES ('k/crash', now() - INTERVAL '1 minute')`)
	ctx, cancel := context.WithCancel(context.Background())

	DeleteRetiredObjects(ctx, pool, cancelOnDelete{newMemStorage(map[string]string{"k/crash": "x"}), cancel})

	if _, ok := retired(t, pool)["k/crash"]; !ok {
		t.Error("a crash during the storage delete lost the record")
	}
}

// stallingDelete is a storage delete that hangs until its context ends.
type stallingDelete struct{ *memStorage }

func (stallingDelete) DeleteObject(ctx context.Context, _ string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Second):
		return nil
	}
}

// A stalled storage delete must not hold the record's lock, its connection
// and the rest of the cleanup loop indefinitely: it times out, and the record
// is pushed back for a later try.
func TestSweepBoundsAStalledDelete(t *testing.T) {
	pool := accountDB(t)
	mustExecDB(t, pool, `INSERT INTO retired_objects (key, delete_after) VALUES ('k/stall', now() - INTERVAL '1 minute')`)
	op := retiredObjectOpTimeout
	retiredObjectOpTimeout = 200 * time.Millisecond
	t.Cleanup(func() { retiredObjectOpTimeout = op })

	start := time.Now()
	DeleteRetiredObjects(context.Background(), pool, stallingDelete{newMemStorage(map[string]string{"k/stall": "x"})})

	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("sweep took %v with a stalled delete, want it bounded", elapsed)
	}
	if due := retired(t, pool)["k/stall"]; due <= 0 {
		t.Errorf("stalled key due in %v, want it kept and pushed back", due)
	}
}
