package mpdevents

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// another starts a further playback session on an already published video.
func another(t *testing.T, db *pgxpool.Pool, id string, preview bool) string {
	t.Helper()
	ctx := context.Background()
	s, err := NewSession(ctx, db, id, preview)
	if err != nil {
		t.Fatal(err)
	}
	if err = Accept(ctx, db, id, Progress{SessionID: s.ID, Playing: true, CurrentTime: 0.5, ElapsedMilliseconds: 500}); err != nil {
		t.Fatal(err)
	}
	return s.ID
}

// watchShared plays a run as watch does, with the watched-share setting given.
func watchShared(t *testing.T, db *pgxpool.Pool, id, session string, from, to float64, share bool) {
	t.Helper()
	for at := from; at < to; at += 5 {
		next := min(at+5, to)
		exec(t, db, `UPDATE mpd_playback_facts SET occurred_at=occurred_at-make_interval(secs=>$2) WHERE session_id=$1`, session, next-at)
		if _, err := Played(context.Background(), db, id, Progress{SessionID: session, Playing: true, PreviousTime: at, CurrentTime: next, ElapsedMilliseconds: int((next - at) * 1000)}, share); err != nil {
			t.Fatal(err)
		}
	}
}

func linkSeconds(t *testing.T, db *pgxpool.Pool, id string) int {
	t.Helper()
	return count(t, db, `SELECT COALESCE(length(replace(string_agg(get_bit(coverage,n)::text,''),'0','')),0)
 FROM mpd_video_watch, generate_series(0,COALESCE(octet_length(coverage),0)*8-1) n WHERE video_id=$1`, id)
}

func eventData(t *testing.T, db *pgxpool.Pool, eventType string) []map[string]any {
	t.Helper()
	rows, err := db.Query(context.Background(), `SELECT convert_from(body,'UTF8')::jsonb->>'data' FROM mpd_event_outbox WHERE event_type=$1 ORDER BY created_at,event_id`, eventType)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(raw), &data); err != nil {
			t.Fatal(err)
		}
		out = append(out, data)
	}
	return out
}

// MPD-460: watched is 80% of the video across every play of the link, not one sitting.
func TestPlaysOfOneLinkAddUpToWatched(t *testing.T) {
	db := testDB(t)
	id, first := published(t, db, 100, false)
	watchShared(t, db, id, first, 0, 45, false)
	second := another(t, db, id, false)
	watchShared(t, db, id, second, 50, 95, false)
	if n := completions(t, db); n != 1 {
		t.Fatalf("two plays of 45%% each made %d completions", n)
	}
	if got := linkSeconds(t, db, id); got != 90 {
		t.Fatalf("link coverage %d, want 90", got)
	}
	data := eventData(t, db, "video.playback_completed")[0]
	// It is sent as the union crosses 80%, partway through the second play.
	if seconds := data["watchedSeconds"].(float64); data["playbackSessionId"] != second || seconds < 80 || seconds > 85 || data["durationSeconds"] != float64(100) {
		t.Fatalf("completed data %v", data)
	}
	if _, ok := data["watchedShare"]; ok {
		t.Fatal("watchedShare sent with the setting off")
	}
	// Neither session played 80% itself, so the players keep reporting.
	if n := count(t, db, `SELECT count(*) FROM mpd_playback_sessions WHERE video_id=$1 AND completed_at IS NOT NULL`, id); n != 0 {
		t.Fatalf("%d sessions marked complete", n)
	}
	third := another(t, db, id, false)
	watchShared(t, db, id, third, 0, 100, false)
	if n := completions(t, db); n != 1 {
		t.Fatalf("completed again: %d", n)
	}
}

func TestTheSameSecondsTwiceCountOnce(t *testing.T) {
	db := testDB(t)
	id, first := published(t, db, 100, false)
	watchShared(t, db, id, first, 0, 45, false)
	second := another(t, db, id, false)
	watchShared(t, db, id, second, 0, 45, false)
	if got := linkSeconds(t, db, id); got != 45 {
		t.Fatalf("link coverage %d, want 45", got)
	}
	if n := completions(t, db); n != 0 {
		t.Fatalf("completed at 45%%: %d", n)
	}
}

func TestStaffPreviewsDoNotCount(t *testing.T) {
	db := testDB(t)
	id, patient := published(t, db, 100, false)
	preview := another(t, db, id, true)
	watchShared(t, db, id, preview, 0, 100, true)
	watchShared(t, db, id, patient, 0, 45, true)
	if got := linkSeconds(t, db, id); got != 45 {
		t.Fatalf("link coverage %d counts the preview", got)
	}
	if n := completions(t, db); n != 0 {
		t.Fatalf("a preview completed the link: %d", n)
	}
}

func TestShareStepsAreSentOnlyWithTheSetting(t *testing.T) {
	db := testDB(t)
	id, session := published(t, db, 100, false)
	watchShared(t, db, id, session, 0, 12, false)
	if n := len(eventData(t, db, "video.playback_progress")); n != 0 {
		t.Fatalf("progress sent with the setting off: %d", n)
	}
	watchShared(t, db, id, session, 12, 33, true)
	progress := eventData(t, db, "video.playback_progress")
	var shares []float64
	for _, data := range progress {
		shares = append(shares, data["watchedShare"].(float64))
	}
	// The first report with the setting on sends the share already reached,
	// then one event per new 5% step: 15 or 17, 20, 25 and 30%.
	if len(shares) != 4 || shares[0] < 0.15 || shares[len(shares)-1] < 0.3 || shares[len(shares)-1] > 0.33 {
		t.Fatalf("progress shares %v", shares)
	}
	for i := 1; i < len(shares); i++ {
		if shares[i] <= shares[i-1] {
			t.Fatalf("progress went backwards: %v", shares)
		}
	}
	// Replaying what is covered sends nothing new.
	watchShared(t, db, id, session, 0, 33, true)
	if n := len(eventData(t, db, "video.playback_progress")); n != len(shares) {
		t.Fatalf("replay sent progress: %d", n)
	}
	watchShared(t, db, id, session, 33, 85, true)
	completed := eventData(t, db, "video.playback_completed")
	if len(completed) != 1 || completed[0]["watchedShare"].(float64) < 0.8 {
		t.Fatalf("completed %v", completed)
	}
}

func TestANewMediaVersionStartsOver(t *testing.T) {
	db := testDB(t)
	id, first := published(t, db, 100, false)
	watchShared(t, db, id, first, 0, 85, false)
	if n := completions(t, db); n != 1 {
		t.Fatalf("completions %d", n)
	}
	exec(t, db, `UPDATE videos SET status='processing' WHERE id=$1`, id)
	exec(t, db, `UPDATE videos SET status='ready',media_version=media_version+1 WHERE id=$1`, id)
	exec(t, db, `UPDATE mpd_video_state SET published=true,published_at=now() WHERE video_id=$1`, id)
	second := another(t, db, id, false)
	watchShared(t, db, id, second, 0, 50, false)
	if n := completions(t, db); n != 1 {
		t.Fatalf("new version inherited the old coverage: %d completions", n)
	}
	watchShared(t, db, id, second, 50, 85, false)
	if n := completions(t, db); n != 2 {
		t.Fatalf("new version not completed on its own: %d", n)
	}
}
