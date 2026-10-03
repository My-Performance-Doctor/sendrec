package video

import (
	"context"
	"slices"
	"testing"
)

// The webcam of a composite abandoned with its process is deleted, and the
// key cleared, once the sweep resets the row. #296.
func TestResetStuckProcessing_DB_DeletesAbandonedWebcam(t *testing.T) {
	pool := accountDB(t)
	name := uniqueName(t)
	user := mustID(t, pool, `INSERT INTO users (email, password, name) VALUES ($1, 'x', 'U') RETURNING id`, name+"@example.com")
	id := seedVideo(t, pool, user, nil, name)
	mustExec(t, pool, `UPDATE videos SET status = 'processing', processing_started_at = now() - interval '3 hours' WHERE id = $1`, id)

	storage := &mockStorage{deleteCalled: make(chan string, 4)}
	resetStuckProcessing(context.Background(), pool, storage)

	if !exists(t, pool, `SELECT 1 FROM videos WHERE id = $1 AND status = 'ready' AND webcam_key IS NULL AND capture_warning = $2`, id, webcamDroppedWarning) {
		t.Error("want the row ready, warned and without its webcam key")
	}
	if got := drainDeletes(storage); !slices.Equal(got, []string{name + "/webcam.webm"}) {
		t.Errorf("want only the webcam deleted, got %v", got)
	}
}

// The sweep removes every object a deleted video references, webcam included,
// before marking it purged. #296.
func TestPurgeOrphanedFiles_DB_DeletesEveryObject(t *testing.T) {
	pool := accountDB(t)
	name := uniqueName(t)
	user := mustID(t, pool, `INSERT INTO users (email, password, name) VALUES ($1, 'x', 'U') RETURNING id`, name+"@example.com")
	id := seedVideo(t, pool, user, nil, name)
	mustExec(t, pool, `UPDATE videos SET status = 'deleted' WHERE id = $1`, id)

	storage := &mockStorage{deleteCalled: make(chan string, 8)}
	PurgeOrphanedFiles(context.Background(), pool, storage)

	if !exists(t, pool, `SELECT 1 FROM videos WHERE id = $1 AND file_purged_at IS NOT NULL`, id) {
		t.Error("want the row marked purged")
	}
	got := drainDeletes(storage)
	for _, want := range []string{"/file.mp4", "/thumb.jpg", "/transcript.vtt", "/webcam.webm"} {
		if !slices.Contains(got, name+want) {
			t.Errorf("want %s deleted, got %v", name+want, got)
		}
	}
}
