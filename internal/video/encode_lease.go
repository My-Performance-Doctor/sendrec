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
//
// Only its holder frees it, through release, which every exit of a claimed
// conversion runs and which matches the claimed time, so a stale holder never
// frees a newer lease. Row writes such as a publish or a recorded failure leave
// the lease alone. An edit, which replaces the file, clears it outright so the
// edited file's conversion can start at once.
type encodeLease struct {
	videoID   string
	startedAt time.Time
	once      sync.Once
}

// inFlightEncodes holds this process's leases so a stopping task can hand them
// back. encodesStopping stops the process claiming once they are handed back.
var (
	inFlightEncodes sync.Map
	encodesStopping atomic.Bool
)

// claimEncode takes the video's encode lease for a conversion of fileKey and
// returns the row's attempts and duration. It returns nil when the row no
// longer points at fileKey, is not ready, no longer needs this conversion, has
// used its attempts, or another conversion holds a lease younger than its
// deadline plus a minute. A dead task's lease still runs out, so the sweep
// recovers its video. The interval is encodeTimeout's, computed in SQL from the
// row's own duration: perJobTimeout plus twice the length, the extra capped.
func claimEncode(ctx context.Context, db database.DBTX, videoID, fileKey, eligible string) (*encodeLease, int, int, error) {
	if encodesStopping.Load() {
		return nil, 0, 0, nil
	}
	var startedAt time.Time
	var attempts, duration int
	err := db.QueryRow(ctx,
		`UPDATE videos SET encode_started_at = now()
		 WHERE id = $1 AND file_key = $2 AND status = 'ready' AND `+eligible+` AND transcode_attempts < $3
		   AND (encode_started_at IS NULL OR encode_started_at < now() - make_interval(secs => $4::float8 + LEAST(GREATEST(2 * duration::bigint, 0), $5::bigint)))
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
		lease.done(ctx, db)
		return nil, 0, 0, nil
	}
	return lease, attempts, duration, nil
}

// logClaimRefused says why a conversion did not start. It reads the row only
// when the claim was refused.
func logClaimRefused(ctx context.Context, db database.DBTX, job, videoID, fileKey string) {
	var status, contentType string
	var sameFile, normalized, leased bool
	var attempts int
	if err := db.QueryRow(ctx,
		`SELECT status, file_key = $2, ios_normalized, content_type, transcode_attempts, encode_started_at IS NOT NULL FROM videos WHERE id = $1`,
		videoID, fileKey,
	).Scan(&status, &sameFile, &normalized, &contentType, &attempts, &leased); err != nil {
		slog.Info(job+": skipped, video gone", "video_id", videoID)
		return
	}
	slog.Info(job+": skipped", "video_id", videoID, "status", status, "same_file", sameFile,
		"ios_normalized", normalized, "content_type", contentType, "attempts", attempts, "leased", leased)
}

// release frees the lease if this conversion still holds it, within ctx's
// deadline. It runs once; later calls do nothing.
func (l *encodeLease) release(ctx context.Context, db database.DBTX) {
	l.once.Do(func() {
		inFlightEncodes.Delete(l)
		if _, err := db.Exec(ctx,
			`UPDATE videos SET encode_started_at = NULL WHERE id = $1 AND encode_started_at = $2`,
			l.videoID, l.startedAt,
		); err != nil {
			slog.Error("encode: failed to release lease", "video_id", l.videoID, "error", err)
		}
	})
}

// done releases the lease at the end of a conversion, on a context of its own:
// the job's may have run out, which is the commonest reason to be here.
func (l *encodeLease) done(ctx context.Context, db database.DBTX) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	l.release(ctx, db)
}

// ReleaseInFlightEncodes hands this process's encode leases back at shutdown,
// as during a deploy or a scale-in, and stops it claiming more. Call it after
// the HTTP server has drained, so a finalize served during the drain still
// claims and is handed back here. Without it a stopped task's video waits out
// the whole encode deadline before another task may convert it. The stopping
// task's own output, if it still finishes, loses nothing: unique output keys
// and the file-key compare-and-swap decide.
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
