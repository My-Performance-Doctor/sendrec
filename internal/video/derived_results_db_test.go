package video

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// editContent replaces the video's content the way an edit's switch does.
func editContent(t *testing.T, pool *pgxpool.Pool, videoID, key string) {
	t.Helper()
	mustExecDB(t, pool, `UPDATE videos SET file_key = $2, media_version = media_version + 1 WHERE id = $1`, videoID, key)
}

type derivedRow struct {
	thumb, transcript, status *string
	version                   int
}

func readDerived(t *testing.T, pool *pgxpool.Pool, videoID string) derivedRow {
	t.Helper()
	var r derivedRow
	if err := pool.QueryRow(context.Background(),
		`SELECT thumbnail_key, transcript_key, transcript_status, media_version FROM videos WHERE id = $1`, videoID,
	).Scan(&r.thumb, &r.transcript, &r.status, &r.version); err != nil {
		t.Fatal(err)
	}
	return r
}

func str(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// #327: a thumbnail is published only for the content it was made from. A
// conversion changes the key, not the content; an edit changes the content.
func TestThumbnailFollowsTheContent(t *testing.T) {
	for _, tc := range []struct {
		name      string
		during    func(t *testing.T, pool *pgxpool.Pool, videoID string)
		published bool
	}{
		{"edit meanwhile", func(t *testing.T, pool *pgxpool.Pool, id string) { editContent(t, pool, id, "recordings/u/tok.e1.webm") }, false},
		{"conversion meanwhile", func(t *testing.T, pool *pgxpool.Pool, id string) { moveKey(t, pool, id, "recordings/u/tok.c1.mp4") }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := accountDB(t)
			const key, oldThumb = "recordings/u/tok.webm", "recordings/u/tok.jpg" // the key every thumbnail used to share
			videoID := seedReadyVideo(t, pool, key, "video/webm")
			mustExecDB(t, pool, `UPDATE videos SET thumbnail_key = $2 WHERE id = $1`, videoID, oldThumb)
			stubFrame(t, func() { tc.during(t, pool, videoID) })
			s := newMemStorage(map[string]string{key: "video", oldThumb: "old thumbnail"})

			GenerateThumbnail(context.Background(), pool, s, videoID, "recordings/u/tok.jpg")

			got := readDerived(t, pool, videoID)
			if s.snapshot()[oldThumb] != "old thumbnail" {
				t.Errorf("the job overwrote the published thumbnail %s", oldThumb)
			}
			if !tc.published {
				if str(got.thumb) != oldThumb {
					t.Errorf("thumbnail_key = %s, want the edit's state left alone", str(got.thumb))
				}
				assertSweptClean(t, pool, s, key, oldThumb)
				return
			}
			if got.thumb == nil || *got.thumb == oldThumb || !strings.HasPrefix(*got.thumb, "recordings/u/tok.") || s.snapshot()[*got.thumb] != "jpeg" {
				t.Errorf("thumbnail_key = %s, want a new key holding the frame", str(got.thumb))
			}
			if due := retired(t, pool)[oldThumb]; due < 90*time.Minute {
				t.Errorf("previous thumbnail due in %v, want it kept for issued URLs", due)
			}
		})
	}
}

// #327: the same for transcripts, including the job's status writes.
func TestTranscriptFollowsTheContent(t *testing.T) {
	for _, tc := range []struct {
		name      string
		during    func(t *testing.T, pool *pgxpool.Pool, videoID string)
		published bool
	}{
		{"edit meanwhile", func(t *testing.T, pool *pgxpool.Pool, id string) {
			editContent(t, pool, id, "recordings/u/tok.e1.webm")
			mustExecDB(t, pool, `UPDATE videos SET transcript_status = 'pending' WHERE id = $1`, id)
		}, false},
		{"conversion meanwhile", func(t *testing.T, pool *pgxpool.Pool, id string) { moveKey(t, pool, id, "recordings/u/tok.c1.mp4") }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TRANSCRIPTION_ENABLED", "true")
			pool := accountDB(t)
			const key, oldVTT = "recordings/u/tok.webm", "recordings/u/tok.vtt" // the key every transcript used to share
			videoID := seedReadyVideo(t, pool, key, "video/webm")
			mustExecDB(t, pool, `UPDATE videos SET transcript_key = $2, transcript_status = 'processing' WHERE id = $1`, videoID, oldVTT)
			audio := extractAudioAt
			extractAudioAt = func(_ context.Context, _, out string) error { return os.WriteFile(out, []byte("wav"), 0o600) }
			t.Cleanup(func() { extractAudioAt = audio })
			tr := hookTranscriber{
				stubTranscriber{available: true, segments: []TranscriptSegment{{Start: 0, End: 1, Text: "hello"}}},
				func() { tc.during(t, pool, videoID) },
			}
			s := newMemStorage(map[string]string{key: "video", oldVTT: "old transcript"})

			processTranscription(context.Background(), pool, s, tr, videoID, key, 0, "u", "tok", "auto", false)

			got := readDerived(t, pool, videoID)
			if s.snapshot()[oldVTT] != "old transcript" {
				t.Errorf("the job overwrote the published transcript %s", oldVTT)
			}
			if !tc.published {
				if str(got.transcript) != oldVTT || str(got.status) != "pending" {
					t.Errorf("transcript = %s, status = %s; want the edit's pending state left alone", str(got.transcript), str(got.status))
				}
				assertSweptClean(t, pool, s, key, oldVTT)
				return
			}
			if got.transcript == nil || *got.transcript == oldVTT || str(got.status) != "ready" {
				t.Errorf("transcript = %s, status = %s; want a new transcript published", str(got.transcript), str(got.status))
			}
		})
	}
}

// #327: the edit's switch invalidates the thumbnail and transcript in the
// same statement, so a crash right after it leaves nothing stale behind, and
// queues the transcript again.
func TestEditSwitchInvalidatesDerivedResults(t *testing.T) {
	t.Setenv("TRANSCRIPTION_ENABLED", "true")
	pool := accountDB(t)
	const key, thumb, vtt = "recordings/u/tok.webm", "recordings/u/tok.t0.jpg", "recordings/u/tok.v0.vtt"
	videoID := seedProcessingVideo(t, pool, key, "video/webm")
	mustExecDB(t, pool, `UPDATE videos SET thumbnail_key = $2, transcript_key = $3, transcript_json = '[]', transcript_status = 'ready' WHERE id = $1`, videoID, thumb, vtt)
	stubEdit(t, "edited")
	ctx, cancel := context.WithCancel(context.Background())
	probe := probeStreamDurations
	probeStreamDurations = func(context.Context, string) (streamDurations, error) {
		cancel() // the process dies right after the switch
		return streamDurations{}, context.Canceled
	}
	t.Cleanup(func() { probeStreamDurations = probe })

	TrimVideoAsync(ctx, pool, newMemStorage(map[string]string{key: "original"}), videoID, key, "recordings/u/tok.jpg", "video/webm", 2, 6)

	got := readDerived(t, pool, videoID)
	if got.thumb != nil || got.transcript != nil || str(got.status) != "pending" || got.version != 1 {
		t.Errorf("after the switch: thumbnail %s, transcript %s, status %s, version %d; want both cleared, pending, version 1",
			str(got.thumb), str(got.transcript), str(got.status), got.version)
	}
	r := retired(t, pool)
	for _, k := range []string{thumb, vtt} {
		if r[k] < 90*time.Minute {
			t.Errorf("%s due in %v, want it retired past issued URLs", k, r[k])
		}
	}
}

// A conversion keeps the content, so it keeps what was derived from it.
func TestConversionKeepsDerivedResults(t *testing.T) {
	pool := accountDB(t)
	const key, thumb = "recordings/u/tok.webm", "recordings/u/tok.t0.jpg"
	videoID := seedReadyVideo(t, pool, key, "video/webm")
	mustExecDB(t, pool, `UPDATE videos SET thumbnail_key = $2 WHERE id = $1`, videoID, thumb)
	stubConversions(t, "converted", nil)

	TranscodeWebMAsync(context.Background(), pool, newMemStorage(map[string]string{key: "original", thumb: "t"}), videoID, key, "")

	if got := readDerived(t, pool, videoID); str(got.thumb) != thumb || got.version != 0 {
		t.Errorf("thumbnail %s, version %d; want them unchanged by a conversion", str(got.thumb), got.version)
	}
}
