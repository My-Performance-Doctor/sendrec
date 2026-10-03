package video

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// #326: upload URLs outlive a deletion. Recording and webcam URLs last 30
// minutes, thumbnail URLs 15, and an upload through one after the purge used
// to recreate the object for good, hidden by file_purged_at.
func TestLateUploadAfterPurgeIsReclaimed(t *testing.T) {
	pool := accountDB(t)
	keys := []string{"recordings/u/tok.webm", "recordings/u/tok_webcam.webm", "recordings/u/tok.t1.jpg", "recordings/u/tok.v1.vtt"}
	videoID := seedReadyVideo(t, pool, keys[0], "video/webm")
	mustExecDB(t, pool, `UPDATE videos SET webcam_key = $2, thumbnail_key = $3, transcript_key = $4, status = 'deleted' WHERE id = $1`,
		videoID, keys[1], keys[2], keys[3])
	objects := map[string]string{}
	for _, k := range keys {
		objects[k] = "original"
	}
	s := newMemStorage(objects)

	PurgeOrphanedFiles(context.Background(), pool, s)
	if left := s.snapshot(); len(left) != 0 {
		t.Fatalf("immediate purge left %v", left)
	}

	// Uploads through URLs issued before the deletion land now.
	for _, k := range keys {
		s.objects[k] = "late upload"
	}
	r := retired(t, pool)
	for _, k := range keys {
		if r[k] < 30*time.Minute {
			t.Errorf("%s due in %v (tracked %t), want a final purge after the longest upload URL", k, r[k], r[k] != 0)
		}
	}

	mustExecDB(t, pool, `UPDATE retired_objects SET delete_after = now() - INTERVAL '1 second'`)
	DeleteRetiredObjects(context.Background(), pool, s)
	if left := s.snapshot(); len(left) != 0 {
		t.Errorf("objects recreated after the purge survived the final purge: %v", left)
	}
}

// The same after an account deletion, which removes the rows altogether.
func TestLateUploadAfterAccountDeletionIsReclaimed(t *testing.T) {
	pool := accountDB(t)
	name := uniqueName(t)
	alice := mustID(t, pool, `INSERT INTO users (email, password, name) VALUES ($1, 'x', 'Alice') RETURNING id`, name+"-alice@example.com")
	seedVideo(t, pool, alice, nil, name+"/alice")
	s := newMemStorage(map[string]string{})

	rec := deleteAccountAs(NewHandler(pool, s, "https://example.com", 0, 0, 0, 0, "secret", false), alice)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d: %s", rec.Code, rec.Body.String())
	}

	s.objects[name+"/alice/file.mp4"] = "late upload"
	if due := retired(t, pool)[name+"/alice/file.mp4"]; due < 30*time.Minute {
		t.Fatalf("file due in %v, want a final purge after the longest upload URL", due)
	}
	mustExecDB(t, pool, `UPDATE retired_objects SET delete_after = now() - INTERVAL '1 second'`)
	DeleteRetiredObjects(context.Background(), pool, s)
	if left := s.snapshot(); len(left) != 0 {
		t.Errorf("objects recreated after the account deletion survived: %v", left)
	}
}
