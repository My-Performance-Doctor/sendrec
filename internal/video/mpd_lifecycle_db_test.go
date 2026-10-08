package video

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sendrec/sendrec/internal/auth"
)

func seedManagedLifecycle(t *testing.T, pool *pgxpool.Pool, status string) (string, string, string) {
	t.Helper()
	user := mustID(t, pool, `INSERT INTO users(email,password,name,retention_days) VALUES('synthetic@example.test','','Synthetic',0) RETURNING id`)
	org := mustID(t, pool, `INSERT INTO organizations(name,slug,retention_days) VALUES('Synthetic','synthetic',0) RETURNING id`)
	mustExec(t, pool, `INSERT INTO mpd_managed_workspaces(organization_id,tenant_id,issuer,enabled,seat_limit) VALUES($1,'33333333-3333-4333-8333-333333333333','https://issuer.example.test',true,10)`, org)
	mustExec(t, pool, `INSERT INTO mpd_external_identities(issuer,subject,user_id,organization_id,staff_id,tenant_id) VALUES('https://issuer.example.test','synthetic',$1,$2,'22222222-2222-4222-8222-222222222222','33333333-3333-4333-8333-333333333333')`, user, org)
	id := mustID(t, pool, `INSERT INTO videos(user_id,organization_id,title,file_key,share_token,status,duration,content_type) VALUES($1,$2,'Synthetic','synthetic.webm','synthetic',$3,60,'video/webm') RETURNING id`, user, org, status)
	return id, user, org
}

func TestMPDRetentionBlockedAndAbandonedUploadDeletionDurable(t *testing.T) {
	pool := accountDB(t)
	ctx := context.Background()
	id, user, org := seedManagedLifecycle(t, pool, "ready")
	mustExec(t, pool, `UPDATE videos SET created_at=now()-interval '60 days',retention_warned_at=now()-interval '8 days' WHERE id=$1`, id)
	if _, err := pool.Exec(ctx, `UPDATE organizations SET retention_days=30 WHERE id=(SELECT organization_id FROM videos WHERE id=$1)`, id); err == nil {
		t.Fatal("managed retention enabled")
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET retention_days=30 WHERE id=$1`, user); err == nil {
		t.Fatal("managed personal retention enabled")
	}
	// Model stale retention restored from pre-guard data in this test's isolated
	// database. Restore the trigger inside the same transaction before running
	// the worker, so production guards remain active for all tested behavior.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `ALTER TABLE organizations DISABLE TRIGGER mpd_workspace_lifecycle`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE organizations SET retention_days=30 WHERE id=$1`, org); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `ALTER TABLE organizations ENABLE TRIGGER mpd_workspace_lifecycle`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// A normal expired video proves that the deletion worker actually ran.
	legacyUser := mustID(t, pool, `INSERT INTO users(email,password,name,retention_days) VALUES('legacy@example.test','','Synthetic',30) RETURNING id`)
	legacyVideo := mustID(t, pool, `INSERT INTO videos(user_id,title,file_key,share_token,status,created_at,retention_warned_at) VALUES($1,'Synthetic','legacy.webm','legacy','ready',now()-interval '60 days',now()-interval '8 days') RETURNING id`, legacyUser)
	processRetentionDeletions(ctx, pool)
	if !exists(t, pool, `SELECT 1 FROM videos WHERE id=$1 AND status='deleted'`, legacyVideo) {
		t.Fatal("unmanaged retention positive control did not delete")
	}
	if !exists(t, pool, `SELECT 1 FROM videos WHERE id=$1 AND status='ready'`, id) {
		t.Fatal("managed retention deleted media")
	}
	// Explicit authorized deletion is a positive control. Durable event precedes purge.
	mustExec(t, pool, `UPDATE videos SET status='deleted' WHERE id=$1`, id)
	storage := newMemStorage(map[string]string{"synthetic.webm": "synthetic", "legacy.webm": "synthetic"})
	PurgeOrphanedFiles(ctx, pool, storage)
	if len(storage.snapshot()) != 0 {
		t.Fatal("authorized tombstone did not purge")
	}
	if !exists(t, pool, `SELECT 1 FROM mpd_event_outbox WHERE video_id=$1 AND event_type='video.deleted'`, id) {
		t.Fatal("purge lost deletion evidence")
	}
	uploading := mustID(t, pool, `INSERT INTO videos(user_id,organization_id,title,file_key,share_token,status,created_at) SELECT user_id,organization_id,'Synthetic','abandoned.webm','abandoned','uploading',now()-interval '25 hours' FROM videos WHERE id=$1 RETURNING id`, id)
	AbandonStaleUploads(ctx, pool)
	if !exists(t, pool, `SELECT 1 FROM mpd_event_outbox WHERE video_id=$1 AND event_type='video.deleted' AND media_version=1`, uploading) {
		t.Fatal("abandoned upload missing versioned deletion")
	}
}

func TestMPDEditRecoveryCannotLetOldFallbackReleaseNewClaim(t *testing.T) {
	pool := accountDB(t)
	ctx := context.Background()
	id, _, _ := seedManagedLifecycle(t, pool, "ready")
	mustExec(t, pool, `UPDATE videos SET status='processing',processing_started_at=now()-interval '1 hour' WHERE id=$1`, id)
	oldCtx := context.WithValue(ctx, editVersionKey{}, 2)
	resetStuckProcessing(ctx, pool, newMemStorage(nil), videoReadyHook{})
	mustExec(t, pool, `UPDATE videos SET status='processing',processing_started_at=now() WHERE id=$1`, id)
	TrimVideoAsync(oldCtx, pool, newMemStorage(nil), id, "synthetic.webm", "synthetic.jpg", "video/webm", 1, 2)
	RemoveSegmentsAsync(oldCtx, pool, newMemStorage(nil), id, "synthetic.webm", "synthetic.jpg", "video/webm", []segmentRange{{Start: 1, End: 2}}, 60)
	CompositeWithWebcam(oldCtx, pool, newMemStorage(nil), id, "synthetic.webm", "synthetic-camera.webm", "synthetic.jpg", "video/webm")
	if !exists(t, pool, `SELECT 1 FROM videos WHERE id=$1 AND status='processing' AND media_version=3`, id) {
		t.Fatal("old fallback released newer claim")
	}
}

func TestMPDDirectTranscriptUploadInvalidatesEarlierJob(t *testing.T) {
	pool := accountDB(t)
	ctx := context.Background()
	id, user, org := seedManagedLifecycle(t, pool, "ready")
	mustExec(t, pool, `UPDATE videos SET transcript_generation=1,transcript_status='processing',transcript_started_at=now() WHERE id=$1`, id)
	storage := newMemStorage(map[string]string{})
	h := NewHandler(pool, storage, testBaseURL, 0, 0, 0, 0, testHMACSecret, false)
	req := authenticatedMultipartRequest(t, "/", "file", "synthetic.vtt", []byte("WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nSynthetic caption\n"))
	req = withURLParam(req.WithContext(auth.ContextWithOrg(auth.ContextWithUserID(req.Context(), user), org, "member")), "id", id)
	rec := httptest.NewRecorder()
	h.UploadTranscript(rec, req)
	if rec.Code != 200 {
		t.Fatalf("upload %d %s", rec.Code, rec.Body.String())
	}
	tag, err := pool.Exec(ctx, `UPDATE videos SET transcript_key='stale.vtt',transcript_published_generation=1 WHERE id=$1 AND transcript_generation=1`, id)
	if err != nil || tag.RowsAffected() != 0 {
		t.Fatalf("stale completion %v %v", tag, err)
	}
	if !exists(t, pool, `SELECT 1 FROM mpd_video_state WHERE video_id=$1 AND transcript_version=2`, id) {
		t.Fatal("direct upload not versioned")
	}
}

// Two ECS tasks can run conversions simultaneously. Unique output keys and the
// file-key compare-and-swap keep the winning publication intact; the losing
// output stays in durable retired_objects for cleanup.
func TestMPDConcurrentConversionsPreserveWinnerAndRecoverLoser(t *testing.T) {
	pool := accountDB(t)
	ctx := context.Background()
	id, _, _ := seedManagedLifecycle(t, pool, "ready")
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	stubConversions(t, "converted", func() { entered <- struct{}{}; <-release })
	storage := newMemStorage(map[string]string{"synthetic.webm": "original"})
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); TranscodeWebMAsync(ctx, pool, storage, id, "synthetic.webm", "") }()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			close(release)
			wg.Wait()
			t.Fatal("conversion did not reach barrier")
		}
	}
	close(release)
	wg.Wait()
	got := readConversionRow(t, pool, id)
	if got.fileKey == "synthetic.webm" {
		t.Fatal("neither conversion published")
	}
	DeleteRetiredObjects(ctx, pool, storage)
	objects := storage.snapshot()
	if objects[got.fileKey] != "converted" || len(objects) != 2 {
		t.Fatalf("winner and grace-period original must survive: %v", objects)
	}
	if !exists(t, pool, `SELECT 1 FROM mpd_video_state WHERE video_id=$1 AND media_version=1`, id) {
		t.Fatal("format conversion changed content version")
	}
}
