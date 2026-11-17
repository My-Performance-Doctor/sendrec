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

// releaseTimeout bounds each lease release, within any shorter parent deadline.
const releaseTimeout = 2 * time.Second

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
	mu        sync.Mutex
	released  bool
}

// inFlightEncodes holds this process's leases so a stopping task can hand them
// back. encodesStopping stops the process claiming once shutdown has begun.
var (
	inFlightEncodes sync.Map
	encodesStopping atomic.Bool
)

// claimEncode takes the video's encode lease for a conversion of fileKey and
// returns the row's duration. It returns nil when the row no longer points at
// fileKey, is not ready, no longer needs this conversion, has used its
// attempts, or another conversion holds a lease younger than its deadline plus
// a minute. A dead task's lease still runs out, so the sweep recovers its
// video.
//
// The deadline is encodeTimeout's: the claim runs before the duration is known
// to Go, so it applies the same constants to the row's own duration.
func claimEncode(ctx context.Context, db database.DBTX, videoID, fileKey, eligible string) (*encodeLease, int, error) {
	if encodesStopping.Load() {
		return nil, 0, nil
	}
	var startedAt time.Time
	var duration int
	err := db.QueryRow(ctx,
		`UPDATE videos SET encode_started_at = now()
		 WHERE id = $1 AND file_key = $2 AND status = 'ready' AND `+eligible+` AND transcode_attempts < $3
		   AND (encode_started_at IS NULL OR encode_started_at < now() - make_interval(secs =>
		        $4::float8 + LEAST(GREATEST($5::bigint * duration::bigint, 0), $6::bigint)))
		 RETURNING encode_started_at, duration`,
		videoID, fileKey, maxTranscodeAttempts,
		(perJobTimeout+time.Minute).Seconds(), encodeSecondsPerSecond, encodeExtraCapSeconds,
	).Scan(&startedAt, &duration)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	lease := &encodeLease{videoID: videoID, startedAt: startedAt}
	inFlightEncodes.Store(lease, struct{}{})
	// Shutdown can begin during the claim, after ReleaseInFlightEncodes found
	// nothing to hand back.
	if encodesStopping.Load() {
		lease.done(ctx, db)
		return nil, 0, nil
	}
	return lease, duration, nil
}

// logClaimRefused says why a conversion did not start. It reads the row only
// when the claim was refused, and not at all once the task is stopping.
func logClaimRefused(ctx context.Context, db database.DBTX, job, videoID, fileKey string) {
	if encodesStopping.Load() {
		slog.Info(job+": skipped, task stopping", "video_id", videoID)
		return
	}
	var status, contentType string
	var sameFile, normalized, leased bool
	var attempts int
	if err := db.QueryRow(ctx,
		`SELECT status, file_key = $2, ios_normalized, content_type, transcode_attempts, encode_started_at IS NOT NULL FROM videos WHERE id = $1`,
		videoID, fileKey,
	).Scan(&status, &sameFile, &normalized, &contentType, &attempts, &leased); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			slog.Info(job+": skipped, video gone", "video_id", videoID)
			return
		}
		slog.Warn(job+": skipped, could not read why", "video_id", videoID, "error", err)
		return
	}
	if status == "ready" && sameFile && !leased && attempts >= maxTranscodeAttempts {
		slog.Warn(job+": skipped, attempt budget exhausted", "video_id", videoID, "attempts", attempts)
		return
	}
	slog.Info(job+": skipped", "video_id", videoID, "status", status, "same_file", sameFile,
		"ios_normalized", normalized, "content_type", contentType, "attempts", attempts, "leased", leased)
}

// release frees the lease if this conversion still holds it. Each try has its
// own short timeout within ctx's deadline. A failed try leaves the lease
// tracked, so a later release (the conversion's deferred done) tries again.
// It reports whether the lease is released.
func (l *encodeLease) release(ctx context.Context, db database.DBTX) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return true
	}
	ctx, cancel := context.WithTimeout(ctx, releaseTimeout)
	defer cancel()
	if _, err := db.Exec(ctx,
		`UPDATE videos SET encode_started_at = NULL WHERE id = $1 AND encode_started_at = $2`,
		l.videoID, l.startedAt,
	); err != nil {
		slog.Warn("encode: lease release failed", "video_id", l.videoID, "error", err)
		return false
	}
	l.released = true
	inFlightEncodes.Delete(l)
	return true
}

// releaseAttempts and releaseBackoff bound done's retries.
const releaseAttempts = 3

var releaseBackoff = 200 * time.Millisecond

// done releases the lease at the end of a conversion, on a context of its own:
// the job's may have run out, which is the commonest reason to be here. A
// release that keeps failing leaves the lease to run out at its deadline.
func (l *encodeLease) done(ctx context.Context, db database.DBTX) {
	ctx = context.WithoutCancel(ctx)
	for attempt := 1; attempt <= releaseAttempts; attempt++ {
		if l.release(ctx, db) {
			return
		}
		if attempt < releaseAttempts {
			time.Sleep(time.Duration(attempt) * releaseBackoff)
		}
	}
	slog.Error("encode: gave up releasing lease; it runs out at its deadline", "video_id", l.videoID, "attempts", releaseAttempts)
}

// ReleaseInFlightEncodes hands this process's encode leases back at shutdown,
// as during a deploy or a scale-in, and stops it claiming more. Without it a
// stopped task's video waits out the whole encode deadline before another task
// may convert it. The stopping task's own output, if it still finishes, loses
// nothing: unique output keys and the file-key compare-and-swap decide.
func ReleaseInFlightEncodes(ctx context.Context, db database.DBTX) {
	releaseInFlightEncodes(ctx, db, nil)
}

// releaseInFlightEncodes releases the tracked leases that only accepts, or all
// of them when only is nil. Tests pass their own videos.
func releaseInFlightEncodes(ctx context.Context, db database.DBTX, only func(videoID string) bool) {
	encodesStopping.Store(true)
	released, failed := 0, 0
	inFlightEncodes.Range(func(key, _ any) bool {
		lease := key.(*encodeLease)
		if only == nil || only(lease.videoID) {
			if lease.release(ctx, db) {
				released++
			} else {
				failed++
			}
		}
		return true
	})
	if released > 0 {
		slog.Info("encode: released running conversions at shutdown", "count", released)
	}
	if failed > 0 {
		slog.Error("encode: could not release running conversions at shutdown; they run out at their deadlines", "count", failed)
	}
}
