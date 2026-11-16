package video

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// A lease lasts the encode's deadline plus a minute, so the sweep never starts
// a second encode beside a live one but still recovers a dead task's video.
func TestEncodeLeaseFollowsTheDeadline(t *testing.T) {
	pool := accountDB(t)
	ctx := context.Background()
	id, _, _ := seedManagedLifecycle(t, pool, "ready")
	claim := func() bool {
		t.Helper()
		claimed, err := claimEncode(ctx, pool, id, 60)
		if err != nil {
			t.Fatal(err)
		}
		return claimed
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
