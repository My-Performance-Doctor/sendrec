package mpdevents

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// published makes a published video of the given length and a session on it,
// a staff preview when preview is set.
func published(t *testing.T, db *pgxpool.Pool, seconds int, preview bool) (string, string) {
	t.Helper()
	user, org := owner(t, db)
	id := video(t, db, user, org, "ready")
	exec(t, db, `UPDATE videos SET duration=$2 WHERE id=$1`, id, seconds)
	exec(t, db, `UPDATE mpd_video_state SET published=true,published_at=now() WHERE video_id=$1`, id)
	s, err := NewSession(context.Background(), db, id, preview)
	if err != nil {
		t.Fatal(err)
	}
	return id, s.ID
}

// step plays from one position to another. The session's last observation is
// moved back by the step's length first, so the server's clock allows its pace.
func step(t *testing.T, db *pgxpool.Pool, id, session string, from, to float64) error {
	t.Helper()
	exec(t, db, `UPDATE mpd_playback_sessions SET last_progress_at=last_progress_at-make_interval(secs=>$2) WHERE session_id=$1`, session, to-from)
	_, err := Played(context.Background(), db, id, Progress{SessionID: session, Playing: true, PreviousTime: from, CurrentTime: to, ElapsedMilliseconds: int((to - from) * 1000)})
	return err
}

// watch plays a contiguous run in steps of at most eight seconds.
func watch(t *testing.T, db *pgxpool.Pool, id, session string, from, to float64) {
	t.Helper()
	for at := from; at < to; at += 8 {
		next := min(at+8, to)
		if err := step(t, db, id, session, at, next); err != nil {
			t.Fatal(err)
		}
	}
}

func completions(t *testing.T, db *pgxpool.Pool) int {
	t.Helper()
	return count(t, db, `SELECT count(*) FROM mpd_event_outbox WHERE event_type='video.playback_completed'`)
}

func TestEightyPercentOfContiguousPlayCompletesOnce(t *testing.T) {
	db := testDB(t)
	id, session := published(t, db, 100, false)

	watch(t, db, id, session, 0, 79)
	if n := completions(t, db); n != 0 {
		t.Fatalf("completed at 79%%: %d", n)
	}
	watch(t, db, id, session, 79, 100)
	if n := completions(t, db); n != 1 {
		t.Fatalf("completions %d", n)
	}
	var data struct {
		ViewerClass    string  `json:"viewerClass"`
		WatchedSeconds float64 `json:"watchedSeconds"`
		Duration       float64 `json:"durationSeconds"`
	}
	body := queryString(t, db, `SELECT convert_from(body,'UTF8')::jsonb->>'data' FROM mpd_event_outbox WHERE event_type='video.playback_completed'`)
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		t.Fatal(err)
	}
	if data.ViewerClass != "anonymous" || data.WatchedSeconds < 80 || data.Duration != 100 {
		t.Fatalf("event data %+v", data)
	}
}

func TestSeekingAheadAddsNothing(t *testing.T) {
	db := testDB(t)
	id, session := published(t, db, 100, false)

	watch(t, db, id, session, 0, 10)
	// The player resets its anchor on a seek, so the next observation starts at the new position.
	watch(t, db, id, session, 90, 100)

	if played := queryString(t, db, `SELECT played_seconds::int::text FROM mpd_playback_sessions WHERE session_id=$1`, session); played != "12" {
		t.Fatalf("played %s", played)
	}
	if n := completions(t, db); n != 0 {
		t.Fatalf("a seek completed the video: %d", n)
	}
}

func TestAReplayedOrFastObservationAddsNothing(t *testing.T) {
	db := testDB(t)
	id, session := published(t, db, 100, false)
	watch(t, db, id, session, 0, 8)

	// The same observation again does not continue from the last position.
	if err := step(t, db, id, session, 0, 8); err != nil {
		t.Fatal(err)
	}
	// Eight seconds of media claimed one millisecond after the last observation runs far faster than the clock.
	exec(t, db, `UPDATE mpd_playback_sessions SET last_progress_at=clock_timestamp() WHERE session_id=$1`, session)
	if _, err := Played(context.Background(), db, id, Progress{SessionID: session, Playing: true, PreviousTime: 8, CurrentTime: 16, ElapsedMilliseconds: 8000}); err != nil {
		t.Fatal(err)
	}

	if played := queryString(t, db, `SELECT played_seconds::int::text FROM mpd_playback_sessions WHERE session_id=$1`, session); played != "8" {
		t.Fatalf("played %s", played)
	}
}

func TestAPreviewNeverCompletes(t *testing.T) {
	db := testDB(t)
	id, session := published(t, db, 20, true)

	watch(t, db, id, session, 0, 20)

	if n := completions(t, db); n != 0 {
		t.Fatalf("a preview completed: %d", n)
	}
}

func TestPlayingKeepsTheSessionOpenAndAnExpiredOneIsRefused(t *testing.T) {
	db := testDB(t)
	id, session := published(t, db, 600, false)
	exec(t, db, `UPDATE mpd_playback_sessions SET expires_at=now()+interval '5 seconds' WHERE session_id=$1`, session)

	watch(t, db, id, session, 0, 8)
	if n := count(t, db, `SELECT count(*) FROM mpd_playback_sessions WHERE session_id=$1 AND expires_at>now()+interval '4 minutes'`, session); n != 1 {
		t.Fatal("playing did not keep the session open")
	}

	exec(t, db, `UPDATE mpd_playback_sessions SET expires_at=now()-interval '1 second' WHERE session_id=$1`, session)
	if err := step(t, db, id, session, 8, 16); err == nil {
		t.Fatal("an expired session accepted progress")
	}
}

func TestAnUnknownLengthNeverCompletes(t *testing.T) {
	db := testDB(t)
	id, session := published(t, db, 0, false)

	watch(t, db, id, session, 0, 40)

	if n := completions(t, db); n != 0 {
		t.Fatalf("completed without a length: %d", n)
	}
}
