package video

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sendrec/sendrec/internal/database"
)

// What makes a video worth each conversion. The claim checks it in the same
// statement that takes the lease, so a conversion that read the row a moment
// earlier cannot start on a video another task has just finished.
const (
	normalizeEligible = "ios_normalized = false"
	transcodeEligible = "content_type = 'video/webm'"
)

// encodeLease is one conversion's hold on a video's encode lease. The sweep on
// each task picked any video older than 5 minutes and repeated encodes still
// running, three at once for one production recording; the lease stops that.
// It is released by the conversion's own publish or recorded failure, by an
// edit replacing the file, by release on any other exit, and at shutdown.
// startedAt guards every release, so a stale holder never frees a newer lease.
type encodeLease struct {
	videoID   string
	startedAt time.Time
	once      sync.Once
}

// inFlightEncodes holds this process's leases so a stopping task can hand them
// back. encodesStopping stops the process claiming once shutdown has begun.
var (
	inFlightEncodes sync.Map
	encodesStopping atomic.Bool
)

// claimEncode takes the video's encode lease for a conversion of fileKey. It
// returns nil when the row no longer points at fileKey, is not ready, no longer
// needs this conversion, has used its attempts, or another conversion holds a
// lease younger than its deadline plus a minute. A dead task's lease still runs
// out, so the sweep recovers its video. On success it returns the row's current
// attempts and duration.
func claimEncode(ctx context.Context, db database.DBTX, videoID, fileKey, eligible string) (*encodeLease, int, int, error) {
	if encodesStopping.Load() {
		return nil, 0, 0, nil
	}
	var startedAt time.Time
	var attempts, duration int
	err := db.QueryRow(ctx,
		`UPDATE videos SET encode_started_at = now()
		 WHERE id = $1 AND file_key = $2 AND status = 'ready' AND `+eligible+` AND transcode_attempts < $3
		   AND (encode_started_at IS NULL OR encode_started_at < now() - make_interval(secs => $4::float8 + LEAST(GREATEST(2 * duration, 0), $5::bigint)))
		 RETURNING encode_started_at, transcode_attempts, duration`,
		videoID, fileKey, maxTranscodeAttempts, (perJobTimeout+time.Minute).Seconds(), encodeExtraCapSeconds,
	).Scan(&startedAt, &attempts, &duration)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, 0, nil
	}
	if err != nil {
		return nil, 0, 0, err
	}
	lease := &encodeLease{videoID: videoID, startedAt: startedAt}
	inFlightEncodes.Store(lease, struct{}{})
	// Shutdown can begin during the claim, after ReleaseInFlightEncodes found
	// nothing to hand back.
	if encodesStopping.Load() {
		lease.release(ctx, db)
		return nil, 0, 0, nil
	}
	return lease, attempts, duration, nil
}

// cleared forgets a lease whose row update already released it: a publish, a
// recorded failure, or a verdict that the file needs nothing. An edit that
// switched the file meanwhile released it too.
func (l *encodeLease) cleared() {
	l.once.Do(func() { inFlightEncodes.Delete(l) })
}

// release frees the lease if this conversion still holds it. Deferred after
// every claim, it covers each exit that wrote nothing to the row.
func (l *encodeLease) release(ctx context.Context, db database.DBTX) {
	l.once.Do(func() {
		inFlightEncodes.Delete(l)
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := db.Exec(ctx,
			`UPDATE videos SET encode_started_at = NULL WHERE id = $1 AND encode_started_at = $2`,
			l.videoID, l.startedAt,
		); err != nil {
			slog.Error("encode: failed to release lease", "video_id", l.videoID, "error", err)
		}
	})
}

// ReleaseInFlightEncodes hands this process's encode leases back at shutdown,
// as during a deploy or a scale-in, and stops it claiming more. Without it a
// stopped task's video waits out the whole encode deadline before another task
// may convert it. The stopping task's own output, if it still finishes, loses
// nothing: unique output keys and the file-key compare-and-swap decide.
func ReleaseInFlightEncodes(ctx context.Context, db database.DBTX) {
	encodesStopping.Store(true)
	n := 0
	inFlightEncodes.Range(func(key, _ any) bool {
		key.(*encodeLease).release(ctx, db)
		n++
		return true
	})
	if n > 0 {
		slog.Info("encode: released running conversions at shutdown", "count", n)
	}
}
