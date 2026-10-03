package video

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
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

// Review #7: a webcam upload URL can outlive the composite that dropped the
// webcam, so the cleared key is retired past the URL's lifetime.
func TestDroppedWebcamIsRetired(t *testing.T) {
	pool := accountDB(t)
	const screen, webcam = "recordings/u/tok.webm", "recordings/u/tok_webcam.webm"
	videoID := seedProcessingVideo(t, pool, screen, "video/webm")
	mustExecDB(t, pool, `UPDATE videos SET webcam_key = $2 WHERE id = $1`, videoID, webcam)
	stubFrame(t, nil)
	stubComposite(t, nil)

	CompositeWithWebcam(context.Background(), pool, newMemStorage(map[string]string{screen: "s", webcam: "w"}), videoID, screen, webcam, "recordings/u/tok.jpg", "video/webm")

	if due := retired(t, pool)[webcam]; due < 30*time.Minute {
		t.Errorf("dropped webcam due in %v, want it retired past the upload URL", due)
	}
}

// Review #6: logos are shared between a workspace's branding and its videos;
// the sweep must not delete one still in use.
func TestSweepKeepsLogosInUse(t *testing.T) {
	pool := accountDB(t)
	videoID := seedReadyVideo(t, pool, "recordings/u/tok.webm", "video/webm")
	var userID string
	if err := pool.QueryRow(context.Background(), `SELECT user_id FROM videos WHERE id = $1`, videoID).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	mustExecDB(t, pool, `UPDATE videos SET branding_logo_key = 'branding/x/video-logo.png' WHERE id = $1`, videoID)
	mustExecDB(t, pool, `INSERT INTO user_branding (user_id, logo_key) VALUES ($1, 'branding/x/logo.png')`, userID)
	mustExecDB(t, pool, `INSERT INTO retired_objects (key, delete_after) VALUES
		('branding/x/video-logo.png', now() - INTERVAL '1 minute'), ('branding/x/logo.png', now() - INTERVAL '1 minute')`)
	s := newMemStorage(map[string]string{"branding/x/video-logo.png": "a", "branding/x/logo.png": "b"})

	DeleteRetiredObjects(context.Background(), pool, s)

	if left := s.snapshot(); len(left) != 2 {
		t.Errorf("logos left = %v, want both kept", left)
	}
}

// Review #5: a workspace logo is shared by everyone's videos in it. Deleting
// one member's account must not delete it from under the others.
func TestAccountDeletionKeepsSharedLogos(t *testing.T) {
	pool := accountDB(t)
	name := uniqueName(t)
	alice := mustID(t, pool, `INSERT INTO users (email, password, name) VALUES ($1, 'x', 'Alice') RETURNING id`, name+"-alice@example.com")
	bob := mustID(t, pool, `INSERT INTO users (email, password, name) VALUES ($1, 'x', 'Bob') RETURNING id`, name+"-bob@example.com")
	shared := mustID(t, pool, `INSERT INTO organizations (name, slug) VALUES ('Shared', $1) RETURNING id`, name+"-shared")
	mustExec(t, pool, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'owner'), ($1, $3, 'member')`, shared, bob, alice)
	logo := "branding/org-" + shared + "/logo.png"
	mustExec(t, pool, `INSERT INTO user_branding (organization_id, logo_key) VALUES ($1, $2)`, shared, logo)
	aliceVideo := seedVideo(t, pool, alice, &shared, name+"/alice")
	mustExec(t, pool, `UPDATE videos SET branding_logo_key = $2 WHERE id = $1`, aliceVideo, logo)
	s := newMemStorage(map[string]string{logo: "workspace logo"})

	rec := deleteAccountAs(NewHandler(pool, s, "https://example.com", 0, 0, 0, 0, "secret", false), alice)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if s.snapshot()[logo] != "workspace logo" {
		t.Error("deleting a member's account deleted the workspace logo")
	}
	DeleteRetiredObjects(context.Background(), pool, s)
	mustExecDB(t, pool, `UPDATE retired_objects SET delete_after = now() - INTERVAL '1 second'`)
	DeleteRetiredObjects(context.Background(), pool, s)
	if s.snapshot()[logo] != "workspace logo" {
		t.Error("the final purge deleted the workspace logo")
	}
}

// failingSchedule fails the statement that schedules the final purge.
type failingSchedule struct{ *pgxpool.Pool }

func (f failingSchedule) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(sql, "INSERT INTO retired_objects") {
		return pgconn.CommandTag{}, errors.New("database unavailable")
	}
	return f.Pool.Exec(ctx, sql, args...)
}

// Review #4: if the final purge can't be scheduled, the account is not
// deleted, so nothing is left unreachable.
func TestAccountDeletionStopsWhenTheFinalPurgeCannotBeScheduled(t *testing.T) {
	pool := accountDB(t)
	name := uniqueName(t)
	alice := mustID(t, pool, `INSERT INTO users (email, password, name) VALUES ($1, 'x', 'Alice') RETURNING id`, name+"-alice@example.com")
	seedVideo(t, pool, alice, nil, name+"/alice")
	s := newMemStorage(map[string]string{name + "/alice/file.mp4": "video"})

	rec := deleteAccountAs(NewHandler(failingSchedule{pool}, s, "https://example.com", 0, 0, 0, 0, "secret", false), alice)

	if rec.Code < 500 {
		t.Errorf("status = %d, want a failure", rec.Code)
	}
	if !exists(t, pool, `SELECT 1 FROM users WHERE id = $1`, alice) {
		t.Error("the account was deleted although its final purge was not scheduled")
	}
	if _, ok := s.snapshot()[name+"/alice/file.mp4"]; !ok {
		t.Error("files were deleted although the account stays")
	}
}
