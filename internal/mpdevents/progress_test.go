package mpdevents

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// published makes a published video of the given length and a started
// session on it, a staff preview when preview is set.
func published(t *testing.T, db *pgxpool.Pool, seconds int, preview bool) (string, string) {
	t.Helper()
	ctx := context.Background()
	user, org := owner(t, db)
	id := video(t, db, user, org, "ready")
	exec(t, db, `UPDATE videos SET duration=$2 WHERE id=$1`, id, seconds)
	exec(t, db, `UPDATE mpd_video_state SET published=true,published_at=now() WHERE video_id=$1`, id)
	s, err := NewSession(ctx, db, id, preview)
	if err != nil {
		t.Fatal(err)
	}
	if err = Accept(ctx, db, id, Progress{SessionID: s.ID, Playing: true, CurrentTime: 0.5, ElapsedMilliseconds: 500}); err != nil {
		t.Fatal(err)
	}
	return id, s.ID
}

// step plays from one position to another. The session's clock is moved back
// by the step's length first, so the server sees it at real-time pace.
func step(t *testing.T, db *pgxpool.Pool, id, session string, from, to float64) error {
	t.Helper()
	exec(t, db, `UPDATE mpd_playback_sessions SET last_progress_at=last_progress_at-make_interval(secs=>$2),
 created_at=created_at-make_interval(secs=>$2) WHERE session_id=$1`, session, to-from)
	_, err := Played(context.Background(), db, id, Progress{SessionID: session, Playing: true, PreviousTime: from, CurrentTime: to, ElapsedMilliseconds: int((to - from) * 1000)})
	return err
}

// watch plays a run in steps of at most five seconds, as the player reports.
func watch(t *testing.T, db *pgxpool.Pool, id, session string, from, to float64) {
	t.Helper()
	for at := from; at < to; at += 5 {
		if err := step(t, db, id, session, at, min(at+5, to)); err != nil {
			t.Fatal(err)
		}
	}
}

func completions(t *testing.T, db *pgxpool.Pool) int {
	t.Helper()
	return count(t, db, `SELECT count(*) FROM mpd_event_outbox WHERE event_type='video.playback_completed'`)
}

func covered(t *testing.T, db *pgxpool.Pool, session string) int {
	t.Helper()
	return count(t, db, `SELECT COALESCE(length(replace(string_agg(get_bit(coverage,n)::text,''),'0','')),0)
 FROM mpd_playback_sessions, generate_series(0,COALESCE(octet_length(coverage),0)*8-1) n WHERE session_id=$1`, session)
}

func TestEightyPercentOfTheVideoCompletesOnce(t *testing.T) {
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
	if data.ViewerClass != "anonymous" || data.WatchedSeconds < 80 || data.WatchedSeconds > 85 || data.Duration != 100 {
		t.Fatalf("event data %+v", data)
	}
}

func TestASeekCountsOnlyWhatIsPlayed(t *testing.T) {
	db := testDB(t)
	id, session := published(t, db, 100, false)

	watch(t, db, id, session, 0, 10)
	watch(t, db, id, session, 90, 100)

	if n := covered(t, db, session); n != 20 {
		t.Fatalf("covered %d", n)
	}
	if n := completions(t, db); n != 0 {
		t.Fatalf("a seek completed the video: %d", n)
	}
}

func TestRewatchingAndReplayingAddNothing(t *testing.T) {
	db := testDB(t)
	id, session := published(t, db, 100, false)

	watch(t, db, id, session, 0, 40)
	watch(t, db, id, session, 0, 40)
	if err := step(t, db, id, session, 35, 40); err != nil {
		t.Fatal(err)
	}

	if n := covered(t, db, session); n != 40 {
		t.Fatalf("covered %d", n)
	}
	if n := completions(t, db); n != 0 {
		t.Fatalf("a rewatch completed the video: %d", n)
	}
}

func TestPlayFasterThanTheClockCoversNothing(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	id, session := published(t, db, 25, false)

	// Straight after the session opened, and straight after an observation, eight seconds of media are too fast.
	if _, err := Played(ctx, db, id, Progress{SessionID: session, Playing: true, PreviousTime: 0, CurrentTime: 8, ElapsedMilliseconds: 8000}); err != nil {
		t.Fatal(err)
	}
	if _, err := Played(ctx, db, id, Progress{SessionID: session, Playing: true, PreviousTime: 8, CurrentTime: 16, ElapsedMilliseconds: 8000}); err != nil {
		t.Fatal(err)
	}

	if n := covered(t, db, session); n != 0 {
		t.Fatalf("covered %d", n)
	}
}

func TestPositionsPastTheEndAddNothing(t *testing.T) {
	db := testDB(t)
	id, session := published(t, db, 10, false)

	watch(t, db, id, session, 9, 30)

	if n := covered(t, db, session); n != 1 {
		t.Fatalf("covered %d", n)
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

func TestPlayingKeepsTheSessionOpenThroughAPause(t *testing.T) {
	db := testDB(t)
	id, session := published(t, db, 600, false)

	watch(t, db, id, session, 0, 5)
	if n := count(t, db, `SELECT count(*) FROM mpd_playback_sessions WHERE session_id=$1 AND expires_at>now()+interval '29 minutes'`, session); n != 1 {
		t.Fatal("playing did not keep the session open")
	}

	exec(t, db, `UPDATE mpd_playback_sessions SET expires_at=now()-interval '1 second' WHERE session_id=$1`, session)
	if err := step(t, db, id, session, 5, 10); !errors.Is(err, ErrPlaybackClosed) {
		t.Fatalf("an expired session: %v", err)
	}
}

func TestASessionThatNeverStartedIsRefused(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	user, org := owner(t, db)
	id := video(t, db, user, org, "ready")
	exec(t, db, `UPDATE mpd_video_state SET published=true,published_at=now() WHERE video_id=$1`, id)
	s, err := NewSession(ctx, db, id, false)
	if err != nil {
		t.Fatal(err)
	}

	if err := step(t, db, id, s.ID, 0, 5); !errors.Is(err, ErrPlaybackClosed) {
		t.Fatalf("progress without a start: %v", err)
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
