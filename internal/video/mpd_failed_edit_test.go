package video

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sendrec/sendrec/internal/auth"
)

func TestMPDFailedEditRestoresOriginalAvailability(t *testing.T) {
	for _, kind := range []string{"trim", "remove-segments", "remove-silence"} {
		t.Run(kind, func(t *testing.T) {
			pool := accountDB(t)
			ctx := context.Background()
			id, _, _ := seedManagedLifecycle(t, pool, "ready")
			mustExec(t, pool, `UPDATE videos SET share_password='synthetic-hash',transcript_generation=1,transcript_published_generation=1,transcript_status='ready',transcript_key='original.vtt',transcript_json='[{"start":0,"end":1,"text":"Synthetic caption"}]' WHERE id=$1`, id)
			mustExec(t, pool, `UPDATE mpd_video_state SET published=true,published_at=now() WHERE video_id=$1`, id)
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "ffmpeg"), []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			storage := newMemStorage(map[string]string{"synthetic.webm": "synthetic original", "original.vtt": "synthetic captions"})
			for version := 2; version <= 3; version++ {
				mustExec(t, pool, `UPDATE videos SET status='processing',processing_started_at=now() WHERE id=$1`, id)
				if !exists(t, pool, `SELECT 1 FROM videos v JOIN mpd_video_state s ON s.video_id=v.id WHERE v.id=$1 AND v.media_version=$2 AND NOT s.published AND v.transcript_status='none'`, id, version) {
					t.Fatal("edit acceptance did not reserve version and withhold stale captions")
				}
				tx, err := pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = tx.Exec(ctx, `UPDATE videos SET status='ready' WHERE id=$1`, id); err != nil {
					t.Fatal(err)
				}
				if err = tx.Rollback(ctx); err != nil {
					t.Fatal(err)
				}
				if exists(t, pool, `SELECT 1 FROM mpd_event_outbox WHERE video_id=$1 AND media_version=$2`, id, version) {
					t.Fatal("rolled back recovery emitted evidence")
				}
				editCtx := context.WithValue(ctx, editVersionKey{}, version)
				if kind == "trim" {
					TrimVideoAsync(editCtx, pool, storage, id, "synthetic.webm", "synthetic.jpg", "video/webm", 1, 30)
				} else {
					RemoveSegmentsAsync(editCtx, pool, storage, id, "synthetic.webm", "synthetic.jpg", "video/webm", []segmentRange{{Start: 1, End: 2}}, 60)
				}
				if !exists(t, pool, `SELECT 1 FROM videos v JOIN mpd_video_state s ON s.video_id=v.id WHERE v.id=$1 AND v.status='ready' AND v.file_key='synthetic.webm' AND v.media_version=$2 AND v.processing_error IS NOT NULL AND v.transcript_status='ready' AND v.transcript_generation=1 AND v.transcript_published_generation=1 AND v.transcript_key='original.vtt' AND v.transcript_json->0->>'text'='Synthetic caption' AND s.published AND s.published_at IS NOT NULL AND s.transcript_version=1 AND s.edit_snapshot IS NULL`, id, version) {
					t.Fatal("failed edit did not atomically restore original availability")
				}
				if !exists(t, pool, `SELECT 1 FROM mpd_event_outbox WHERE video_id=$1 AND media_version=$2 GROUP BY video_id HAVING count(*) FILTER(WHERE event_type='video.ready')=1 AND count(*) FILTER(WHERE event_type='video.transcript_ready')=1`, id, version) {
					t.Fatal("missing atomic recovery readiness facts")
				}
				// The old transcript worker cannot complete against the reserved version.
				tag, err := pool.Exec(ctx, `UPDATE videos SET transcript_key='stale.vtt' WHERE id=$1 AND media_version=$2`, id, version-1)
				if err != nil || tag.RowsAffected() != 0 {
					t.Fatal("stale worker changed restored content")
				}
			}
		})
	}
}

func TestMPDFailedEditDoesNotOverrideNewPolicyOrSuccessfulContent(t *testing.T) {
	for _, change := range []string{"unpublish", "password", "new-content"} {
		t.Run(change, func(t *testing.T) {
			pool := accountDB(t)
			ctx := context.Background()
			id, _, _ := seedManagedLifecycle(t, pool, "ready")
			mustExec(t, pool, `UPDATE videos SET share_password='original-hash',transcript_generation=1,transcript_published_generation=1,transcript_status='ready',transcript_key='original.vtt' WHERE id=$1`, id)
			mustExec(t, pool, `UPDATE mpd_video_state SET published=true,published_at=now() WHERE video_id=$1`, id)
			mustExec(t, pool, `UPDATE videos SET status='processing' WHERE id=$1`, id)
			switch change {
			case "unpublish":
				mustExec(t, pool, `UPDATE mpd_video_state SET published=false,published_at=NULL WHERE video_id=$1`, id)
			case "password":
				mustExec(t, pool, `UPDATE videos SET share_password=NULL WHERE id=$1`, id)
			case "new-content":
				mustExec(t, pool, `UPDATE videos SET file_key='edited.webm',status='ready',media_version=media_version+1,transcript_key=NULL,transcript_json=NULL,transcript_status='none' WHERE id=$1`, id)
			}
			if change != "new-content" {
				TrimVideoAsync(context.WithValue(ctx, editVersionKey{}, 2), pool, newMemStorage(nil), id, "synthetic.webm", "synthetic.jpg", "video/webm", 1, 2)
			}
			if !exists(t, pool, `SELECT 1 FROM videos v JOIN mpd_video_state s ON s.video_id=v.id WHERE v.id=$1 AND v.status='ready' AND v.media_version=2 AND NOT s.published AND s.edit_snapshot IS NULL`, id) {
				t.Fatal("recovery overrode explicit policy or retained snapshot")
			}
			if change == "new-content" && exists(t, pool, `SELECT 1 FROM mpd_event_outbox WHERE video_id=$1 AND media_version=2 AND event_type='video.transcript_ready'`, id) {
				t.Fatal("successful edit republished old captions")
			}
		})
	}
}

func TestMPDFailedSilenceDetectionDoesNotAcceptEdit(t *testing.T) {
	pool := accountDB(t)
	id, user, org := seedManagedLifecycle(t, pool, "ready")
	mustExec(t, pool, `UPDATE mpd_video_state SET published=true WHERE video_id=$1`, id)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ffmpeg"), []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	h := NewHandler(pool, &mockStorage{downloadURL: "https://synthetic.example.test/media"}, testBaseURL, 0, 0, 0, 0, testHMACSecret, false)
	req := withURLParam(httptest.NewRequest("POST", "/api/videos/"+id+"/detect-silence", strings.NewReader(`{}`)), "id", id)
	req = req.WithContext(auth.ContextWithOrg(auth.ContextWithUserID(req.Context(), user), org, "admin"))
	rec := httptest.NewRecorder()
	h.DetectSilence(rec, req)
	if rec.Code != 500 {
		t.Fatalf("detection failure status %d", rec.Code)
	}
	if !exists(t, pool, `SELECT 1 FROM videos v JOIN mpd_video_state s ON s.video_id=v.id WHERE v.id=$1 AND v.status='ready' AND v.media_version=1 AND s.published AND s.edit_snapshot IS NULL`, id) {
		t.Fatal("failed analysis mutated recording")
	}
}

func TestMPDEditDeletionDropsRecoverySnapshot(t *testing.T) {
	pool := accountDB(t)
	id, _, _ := seedManagedLifecycle(t, pool, "ready")
	mustExec(t, pool, `UPDATE videos SET status='processing' WHERE id=$1`, id)
	if !exists(t, pool, `SELECT 1 FROM mpd_video_state WHERE video_id=$1 AND edit_snapshot IS NOT NULL`, id) {
		t.Fatal("missing accepted edit snapshot")
	}
	mustExec(t, pool, `DELETE FROM videos WHERE id=$1`, id)
	if !exists(t, pool, `SELECT 1 FROM mpd_video_state WHERE video_id=$1 AND deleted_at IS NOT NULL AND edit_snapshot IS NULL`, id) {
		t.Fatal("deletion retained mutable recovery state")
	}
	if !exists(t, pool, `SELECT 1 FROM mpd_event_outbox WHERE video_id=$1 AND media_version=2 AND event_type='video.deleted'`, id) {
		t.Fatal("deletion lost reserved-version evidence")
	}
}
