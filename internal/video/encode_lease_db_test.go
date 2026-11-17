package video

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/sendrec/sendrec/internal/database"
)

// A lease lasts the encode's deadline plus a minute, so the sweep never starts
// a second encode beside a live one but still recovers a dead task's video.
func TestEncodeLeaseFollowsTheDeadline(t *testing.T) {
	pool := accountDB(t)
	ctx := context.Background()
	id, _, _ := seedManagedLifecycle(t, pool, "ready")
	claim := func() bool {
		t.Helper()
		lease, _, _, err := claimEncode(ctx, pool, id, "synthetic.webm", transcodeEligible)
		if err != nil {
			t.Fatal(err)
		}
		if lease != nil {
			lease.cleared()
		}
		return lease != nil
	}
	age := func(d time.Duration) {
		t.Helper()
		mustExec(t, pool, fmt.Sprintf(`UPDATE videos SET encode_started_at=now()-interval '%d seconds' WHERE id=$1`, int(d.Seconds())), id)
	}

	if !claim() {
		t.Fatal("first conversion could not take a free lease")
	}
	if claim() {
		t.Fatal("second conversion took a live lease")
	}
	age(encodeTimeout(60) + 30*time.Second)
	if claim() {
		t.Fatal("lease taken inside its minute of grace after the deadline")
	}
	age(encodeTimeout(60) + 90*time.Second)
	if !claim() {
		t.Fatal("expired lease was not taken over")
	}

	// A failure releases it, so the next sweep can retry at once.
	recordTranscodeFailure(ctx, pool, id, "synthetic.webm", errors.New("synthetic failure"))
	if !exists(t, pool, `SELECT 1 FROM videos WHERE id=$1 AND encode_started_at IS NULL`, id) {
		t.Fatal("recorded failure kept the lease")
	}
}

// Publishing a conversion releases its lease.
func TestPublishedConversionReleasesTheLease(t *testing.T) {
	pool := accountDB(t)
	ctx := context.Background()
	id, _, _ := seedManagedLifecycle(t, pool, "ready")
	stubConversions(t, "converted", nil)

	TranscodeWebMAsync(ctx, pool, newMemStorage(map[string]string{"synthetic.webm": "original"}), id, "synthetic.webm", "")

	if !exists(t, pool, `SELECT 1 FROM videos WHERE id=$1 AND content_type='video/mp4' AND encode_started_at IS NULL`, id) {
		t.Fatal("published conversion kept the lease")
	}
}

// A conversion that lost to an edit held its lease until the deadline ran out,
// so the edited file waited up to about two hours for its own conversion. The
// edit's switch releases it, and the stale conversion's release cannot free
// the lease the edited file's conversion takes next.
func TestEditReleasesALosingConversionsLease(t *testing.T) {
	pool := accountDB(t)
	ctx := context.Background()
	const key = "recordings/u/tok.webm"
	videoID := seedProcessingVideo(t, pool, key, "video/webm")
	// The conversion of the original file still holds its lease.
	stale := &encodeLease{videoID: videoID}
	if err := pool.QueryRow(ctx, `UPDATE videos SET encode_started_at = now() - interval '1 minute' WHERE id = $1 RETURNING encode_started_at`, videoID).Scan(&stale.startedAt); err != nil {
		t.Fatal(err)
	}
	stubEdit(t, "edited")

	TrimVideoAsync(ctx, pool, newMemStorage(map[string]string{key: "original"}), videoID, key, "recordings/u/tok.jpg", "video/webm", 2, 6)

	edited := readEditRow(t, pool, videoID).fileKey
	if edited == key {
		t.Fatal("the edit did not switch the file")
	}
	lease, _, _, err := claimEncode(ctx, pool, videoID, edited, transcodeEligible)
	if err != nil || lease == nil {
		t.Fatalf("the edited file's conversion could not claim at once: lease %v, err %v", lease, err)
	}
	defer lease.cleared()
	stale.release(ctx, pool)
	if !exists(t, pool, `SELECT 1 FROM videos WHERE id = $1 AND encode_started_at IS NOT NULL`, videoID) {
		t.Fatal("the stale conversion's release freed the edited file's lease")
	}
}

// A claim checks the job's file key in the same statement, so a sweep row read
// before an edit cannot convert the replaced file.
func TestClaimRefusesAReplacedFile(t *testing.T) {
	pool := accountDB(t)
	id, _, _ := seedManagedLifecycle(t, pool, "ready")
	lease, _, _, err := claimEncode(context.Background(), pool, id, "replaced.webm", transcodeEligible)
	if err != nil || lease != nil {
		t.Fatalf("claimed a file the row no longer points at: lease %v, err %v", lease, err)
	}
}

// A stopping task hands back only the leases it still holds, and claims
// nothing more.
func TestShutdownReleasesOnlyItsOwnLeases(t *testing.T) {
	t.Cleanup(func() { encodesStopping.Store(false) })
	pool := accountDB(t)
	ctx := context.Background()
	mine, _, _ := seedManagedLifecycle(t, pool, "ready")
	lease, _, _, err := claimEncode(ctx, pool, mine, "synthetic.webm", transcodeEligible)
	if err != nil || lease == nil {
		t.Fatalf("claim: lease %v, err %v", lease, err)
	}
	// Another video, leased by another task.
	other := mustID(t, pool, `INSERT INTO videos(user_id,organization_id,title,file_key,share_token,status,duration,content_type,encode_started_at)
		SELECT user_id,organization_id,'Other','other.webm','other','ready',60,'video/webm',now() FROM videos WHERE id=$1 RETURNING id`, mine)

	ReleaseInFlightEncodes(ctx, pool)

	if !exists(t, pool, `SELECT 1 FROM videos WHERE id = $1 AND encode_started_at IS NULL`, mine) {
		t.Fatal("this task's lease was not handed back")
	}
	if !exists(t, pool, `SELECT 1 FROM videos WHERE id = $1 AND encode_started_at IS NOT NULL`, other) {
		t.Fatal("another task's lease was freed")
	}
	if again, _, _, err := claimEncode(ctx, pool, mine, "synthetic.webm", transcodeEligible); err != nil || again != nil {
		t.Fatalf("a stopping task claimed again: lease %v, err %v", again, err)
	}
}

// An exit that writes nothing to the row, here a missing output file, still
// releases the lease, so the next sweep tick retries instead of waiting out
// the deadline.
func TestEarlyReturnReleasesTheLease(t *testing.T) {
	pool := accountDB(t)
	ctx := context.Background()
	id, _, _ := seedManagedLifecycle(t, pool, "ready")
	original := transcodeToMP4
	transcodeToMP4 = func(_ context.Context, _, out, _ string) error { return os.Remove(out) }
	t.Cleanup(func() { transcodeToMP4 = original })

	TranscodeWebMAsync(ctx, pool, newMemStorage(map[string]string{"synthetic.webm": "original"}), id, "synthetic.webm", "")

	if !exists(t, pool, `SELECT 1 FROM videos WHERE id = $1 AND encode_started_at IS NULL AND content_type = 'video/webm'`, id) {
		t.Fatal("the early return kept the lease")
	}
}

// The sweep recovers a conversion with the noise filter the finalize path
// chose, recorded on the video, and without it for a video that had none.
func TestSweepKeepsTheNoiseFilter(t *testing.T) {
	const filter = "arnndn=m=/app/models/std.rnnn"
	prev := noiseReductionFilter.Load()
	f := filter
	noiseReductionFilter.Store(&f)
	t.Cleanup(func() { noiseReductionFilter.Store(prev) })
	for _, tc := range []struct {
		name, contentType string
		sweep             func(context.Context, database.DBTX, ObjectStorage)
		stub              *func(context.Context, string, string, string) error
	}{
		{"transcode", "video/webm", transcodeExistingWebM, &transcodeToMP4},
		{"normalize", "video/mp4", normalizeExistingVideos, &transcodeToIOSCompatible},
	} {
		for _, denoise := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/noise_reduction=%v", tc.name, denoise), func(t *testing.T) {
				pool := accountDB(t)
				id, _, _ := seedManagedLifecycle(t, pool, "ready")
				mustExec(t, pool, `UPDATE videos SET content_type=$2, noise_reduction=$3, created_at=now()-interval '1 hour' WHERE id=$1`, id, tc.contentType, denoise)
				stubConversions(t, "converted", nil)
				var got []string
				*tc.stub = func(_ context.Context, _, out, audioFilter string) error {
					got = append(got, audioFilter)
					return os.WriteFile(out, []byte("converted"), 0o600)
				}

				tc.sweep(context.Background(), pool, newMemStorage(map[string]string{"synthetic.webm": "original"}))

				want := ""
				if denoise {
					want = filter
				}
				if len(got) != 1 || got[0] != want {
					t.Fatalf("sweep ran the conversion with filters %q, want [%q]", got, want)
				}
			})
		}
	}
}
