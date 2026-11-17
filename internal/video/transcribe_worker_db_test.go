package video

import (
	"context"
	"testing"
)

// The stuck-job reset gives each row the deadline its own recording got, so a
// long recording still inside its time is not started a second time, and a
// short one whose worker died is requeued at the base deadline.
func TestTranscriptionResetFollowsTheRecording(t *testing.T) {
	pool := accountDB(t)
	seed := func(token string, duration, startedMinutesAgo int) string {
		id := seedReadyVideoNamed(t, pool, "recordings/u/"+token+".mp4", token)
		mustExecDB(t, pool, `UPDATE videos SET transcript_status = 'processing', duration = $2,
			transcript_started_at = now() - make_interval(mins => $3) WHERE id = $1`, id, duration, startedMinutesAgo)
		return id
	}
	longRunning := seed("long", 600, 45) // 10 minutes: 70 minute deadline
	shortDead := seed("short", 0, 45)    // unprobed: 30 minute deadline
	longDead := seed("longdead", 600, 75)

	// Every slot held, so local Whisper's worker resets and claims nothing.
	holdEncoderSlots(t, 1)
	processNextTranscription(context.Background(), pool, &mockStorage{}, newLocalWhisper(), false)

	for id, want := range map[string]string{longRunning: "processing", shortDead: "pending", longDead: "pending"} {
		var got string
		if err := pool.QueryRow(context.Background(), `SELECT transcript_status FROM videos WHERE id = $1`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("video %s: transcript_status = %q, want %q", id, got, want)
		}
	}
}

func TestRequeueInFlightTranscriptionAgainstTheDatabase(t *testing.T) {
	pool := accountDB(t)
	t.Cleanup(func() {
		transcriptionStopping.Store(false)
		inFlightTranscription.Store(nil)
	})
	id := seedReadyVideoNamed(t, pool, "recordings/u/running.mp4", "running")
	mustExecDB(t, pool, `UPDATE videos SET transcript_status = 'processing', transcript_generation = 3, transcript_started_at = now() WHERE id = $1`, id)
	inFlightTranscription.Store(&transcriptionJob{videoID: id, generation: 3})

	RequeueInFlightTranscription(context.Background(), pool)

	var status string
	var generation int
	if err := pool.QueryRow(context.Background(), `SELECT transcript_status, transcript_generation FROM videos WHERE id = $1`, id).Scan(&status, &generation); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || generation != 4 {
		t.Errorf("after requeue: status %q generation %d, want pending and 4", status, generation)
	}
}
