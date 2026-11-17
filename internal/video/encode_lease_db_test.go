package video

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v5"
	"github.com/sendrec/sendrec/internal/database"
)

// A lease lasts the encode's deadline plus a minute, so the sweep never starts
// a second encode beside a live one but still recovers a dead task's video.
func TestEncodeLeaseFollowsTheDeadline(t *testing.T) {
	pool := accountDB(t)
	ctx := context.Background()
	id, _, _ := seedManagedLifecycle(t, pool, "ready")
	for _, d := range []int{0, 600, 10 * 3600} {
		mustExec(t, pool, `UPDATE videos SET duration=$2, encode_started_at=now()-make_interval(secs => $3::float8) WHERE id=$1`, id, d, (encodeTimeout(d) + 30*time.Second).Seconds())
		if lease, _, err := claimEncode(ctx, pool, id, "synthetic.webm", transcodeEligible); err != nil || lease != nil {
			t.Fatalf("duration %d: took a lease inside encodeTimeout plus a minute: lease %v, err %v", d, lease, err)
		}
		mustExec(t, pool, `UPDATE videos SET encode_started_at=now()-make_interval(secs => $2::float8) WHERE id=$1`, id, (encodeTimeout(d) + 90*time.Second).Seconds())
		lease, _, err := claimEncode(ctx, pool, id, "synthetic.webm", transcodeEligible)
		if err != nil || lease == nil {
			t.Fatalf("duration %d: expired lease not taken over: lease %v, err %v", d, lease, err)
		}
		lease.release(ctx, pool)
	}
	mustExec(t, pool, `UPDATE videos SET duration=60 WHERE id=$1`, id)
	claim := func() bool {
		t.Helper()
		lease, _, err := claimEncode(ctx, pool, id, "synthetic.webm", transcodeEligible)
		if err != nil {
			t.Fatal(err)
		}
		if lease != nil {
			// Keep the lease in the row; only stop tracking it here.
			inFlightEncodes.Delete(lease)
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

	// A failure counts an attempt for the holder and leaves the lease to its
	// release, which frees it so the next sweep can retry at once.
	var held time.Time
	if err := pool.QueryRow(ctx, `SELECT encode_started_at FROM videos WHERE id=$1`, id).Scan(&held); err != nil {
		t.Fatal(err)
	}
	recordTranscodeFailure(ctx, pool, id, "synthetic.webm", errors.New("synthetic failure"), held)
	if !exists(t, pool, `SELECT 1 FROM videos WHERE id=$1 AND transcode_attempts=1 AND encode_started_at IS NOT NULL`, id) {
		t.Fatal("the holder's failure was not counted, or freed the lease itself")
	}
	(&encodeLease{videoID: id, startedAt: held}).release(ctx, pool)
	if !exists(t, pool, `SELECT 1 FROM videos WHERE id=$1 AND encode_started_at IS NULL`, id) {
		t.Fatal("the holder's release kept the lease")
	}
	recordTranscodeFailure(ctx, pool, id, "synthetic.webm", errors.New("late failure"), held)
	if !exists(t, pool, `SELECT 1 FROM videos WHERE id=$1 AND transcode_attempts=1`, id) {
		t.Fatal("a failure from a conversion that no longer holds the lease was counted")
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
	lease, _, err := claimEncode(ctx, pool, videoID, edited, transcodeEligible)
	if err != nil || lease == nil {
		t.Fatalf("the edited file's conversion could not claim at once: lease %v, err %v", lease, err)
	}
	defer lease.release(ctx, pool)
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
	lease, _, err := claimEncode(context.Background(), pool, id, "replaced.webm", transcodeEligible)
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
	lease, _, err := claimEncode(ctx, pool, mine, "synthetic.webm", transcodeEligible)
	if err != nil || lease == nil {
		t.Fatalf("claim: lease %v, err %v", lease, err)
	}
	// Another video, leased by another task.
	other := mustID(t, pool, `INSERT INTO videos(user_id,organization_id,title,file_key,share_token,status,duration,content_type,encode_started_at)
		SELECT user_id,organization_id,'Other','other.webm','other','ready',60,'video/webm',now() FROM videos WHERE id=$1 RETURNING id`, mine)

	releaseInFlightEncodes(ctx, pool, func(id string) bool { return id == mine || id == other })

	if !exists(t, pool, `SELECT 1 FROM videos WHERE id = $1 AND encode_started_at IS NULL`, mine) {
		t.Fatal("this task's lease was not handed back")
	}
	if !exists(t, pool, `SELECT 1 FROM videos WHERE id = $1 AND encode_started_at IS NOT NULL`, other) {
		t.Fatal("another task's lease was freed")
	}
	if again, _, err := claimEncode(ctx, pool, mine, "synthetic.webm", transcodeEligible); err != nil || again != nil {
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

// A publish that missed because an edit was running leaves the row to the
// edit; the conversion's own release frees the lease, so when the edit falls
// back to 'ready' on the same file the next conversion claims at once.
func TestMissedPublishDuringAnEditReleasesTheLease(t *testing.T) {
	pool := accountDB(t)
	ctx := context.Background()
	id, _, _ := seedManagedLifecycle(t, pool, "ready")
	stubConversions(t, "converted", func() {
		mustExec(t, pool, `UPDATE videos SET status='processing' WHERE id=$1`, id)
	})

	TranscodeWebMAsync(ctx, pool, newMemStorage(map[string]string{"synthetic.webm": "original"}), id, "synthetic.webm", "")

	if !exists(t, pool, `SELECT 1 FROM videos WHERE id=$1 AND file_key='synthetic.webm' AND encode_started_at IS NULL`, id) {
		t.Fatal("the conversion that missed its publish kept the lease")
	}
	// The edit fails and falls back to 'ready' on the same file.
	mustExec(t, pool, `UPDATE videos SET status='ready' WHERE id=$1`, id)
	lease, _, err := claimEncode(ctx, pool, id, "synthetic.webm", transcodeEligible)
	if err != nil || lease == nil {
		t.Fatalf("the next conversion could not claim at once: lease %v, err %v", lease, err)
	}
	lease.release(ctx, pool)
}

// A stale holder, whose lease ran out and was taken over, neither counts its
// failure against the file nor frees the newer lease.
func TestStaleHolderLeavesANewerLeaseAlone(t *testing.T) {
	pool := accountDB(t)
	ctx := context.Background()
	id, _, _ := seedManagedLifecycle(t, pool, "ready")
	stale, _, err := claimEncode(ctx, pool, id, "synthetic.webm", transcodeEligible)
	if err != nil || stale == nil {
		t.Fatalf("claim: lease %v, err %v", stale, err)
	}
	inFlightEncodes.Delete(stale)
	mustExec(t, pool, `UPDATE videos SET encode_started_at = encode_started_at + interval '2 hours' WHERE id=$1`, id)

	recordTranscodeFailure(ctx, pool, id, "synthetic.webm", errors.New("synthetic failure"), stale.startedAt)
	stale.release(ctx, pool)

	if !exists(t, pool, `SELECT 1 FROM videos WHERE id=$1 AND transcode_attempts=0 AND encode_started_at IS NOT NULL`, id) {
		t.Fatal("the stale holder counted its failure or freed the newer lease")
	}
}

// A bogus duration must not overflow the lease interval and fail every claim.
func TestClaimWithAHugeDuration(t *testing.T) {
	pool := accountDB(t)
	ctx := context.Background()
	id, _, _ := seedManagedLifecycle(t, pool, "ready")
	mustExec(t, pool, `UPDATE videos SET duration=2147483647 WHERE id=$1`, id)
	lease, _, err := claimEncode(ctx, pool, id, "synthetic.webm", transcodeEligible)
	if err != nil || lease == nil {
		t.Fatalf("claim with a huge duration: lease %v, err %v", lease, err)
	}
	inFlightEncodes.Delete(lease)
	if again, _, err := claimEncode(ctx, pool, id, "synthetic.webm", transcodeEligible); err != nil || again != nil {
		t.Fatalf("second claim beside a live lease: lease %v, err %v", again, err)
	}
}

// A release that fails keeps the lease tracked, so the conversion's deferred
// release can try again.
func TestFailedReleaseCanBeRetried(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	lease := &encodeLease{videoID: "video-1", startedAt: time.Now()}
	inFlightEncodes.Store(lease, struct{}{})
	t.Cleanup(func() { inFlightEncodes.Delete(lease) })
	mock.ExpectExec(`UPDATE videos SET encode_started_at = NULL`).WithArgs("video-1", lease.startedAt).WillReturnError(errors.New("connection reset"))
	mock.ExpectExec(`UPDATE videos SET encode_started_at = NULL`).WithArgs("video-1", lease.startedAt).WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	lease.release(context.Background(), mock)
	if _, tracked := inFlightEncodes.Load(lease); !tracked {
		t.Fatal("a failed release stopped tracking the lease")
	}
	lease.done(context.Background(), mock)
	if _, tracked := inFlightEncodes.Load(lease); tracked {
		t.Fatal("the retried release left the lease tracked")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// done retries a failing release before giving the lease up to its deadline.
func TestDoneRetriesTheRelease(t *testing.T) {
	prev := releaseBackoff
	releaseBackoff = time.Millisecond
	t.Cleanup(func() { releaseBackoff = prev })
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	lease := &encodeLease{videoID: "video-1", startedAt: time.Now()}
	inFlightEncodes.Store(lease, struct{}{})
	t.Cleanup(func() { inFlightEncodes.Delete(lease) })
	for range releaseAttempts - 1 {
		mock.ExpectExec(`UPDATE videos SET encode_started_at = NULL`).WithArgs("video-1", lease.startedAt).WillReturnError(errors.New("connection reset"))
	}
	mock.ExpectExec(`UPDATE videos SET encode_started_at = NULL`).WithArgs("video-1", lease.startedAt).WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	lease.done(context.Background(), mock)

	if _, tracked := inFlightEncodes.Load(lease); tracked {
		t.Fatal("the retried release left the lease tracked")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
