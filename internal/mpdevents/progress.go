package mpdevents

import (
	"context"
	"errors"
	"math"
	"math/bits"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sendrec/sendrec/internal/database"
)

// WatchedShare is the share of a video one playback session must play for MPD
// to count the video as watched (MPD-460).
const WatchedShare = 0.8

// sessionLife is how long a playback session stays open after its last
// accepted progress, so a viewer can pause and come back without losing it.
const sessionLife = 30 * time.Minute

// maxPace is how much faster than the server's clock media time may advance.
const maxPace = 4

// ErrPlaybackClosed means the session expired, never started, the media
// changed or the video stopped being playable.
var ErrPlaybackClosed = errors.New("playback session expired or media changed")

// ErrInvalidProgress means the observation was not advancing playback.
var ErrInvalidProgress = errors.New("advancing playback is required")

type beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// cover marks the whole seconds from `from` to `to` that lie within the video,
// in a bitmap of one bit per second, and returns how many are now marked.
func cover(bitmap []byte, from, to float64, duration int) int {
	first := max(0, int(math.Floor(from)))
	last := min(duration, int(math.Ceil(to)))
	for second := first; second < last; second++ {
		bitmap[second/8] |= 1 << (second % 8)
	}
	marked := 0
	for _, b := range bitmap {
		marked += bits.OnesCount8(b)
	}
	return marked
}

// Played records one progress observation of a started, open session: the
// seconds it covers, within the video's length, join the session's coverage.
// Seconds already covered, and any stretch skipped by a seek, add nothing. An
// observation that advances media time more than maxPace times faster than
// the server's clock since the session's previous observation, or since it
// opened, covers nothing. Progress keeps the session open for sessionLife.
// Once a non-preview session covers WatchedShare of the video it enqueues
// video.playback_completed, once per session. It returns when the session now
// expires.
func Played(ctx context.Context, db database.DBTX, videoID string, p Progress) (time.Time, error) {
	if !p.Valid() {
		return time.Time{}, ErrInvalidProgress
	}
	b, ok := db.(beginner)
	if !ok {
		return time.Time{}, errors.New("playback progress needs a transaction")
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// A shared lock is enough to fence deletion and media changes, and lets
	// viewers of the same video report at once.
	var version, duration int
	var published bool
	err = tx.QueryRow(ctx, `SELECT s.media_version,v.duration,s.published FROM videos v JOIN mpd_video_state s ON s.video_id=v.id
 WHERE v.id=$1 AND v.status='ready' AND s.deleted_at IS NULL FOR SHARE OF v,s`, videoID).Scan(&version, &duration, &published)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, ErrPlaybackClosed
	} else if err != nil {
		return time.Time{}, err
	}

	var class string
	var coverage []byte
	var since float64
	var completed bool
	err = tx.QueryRow(ctx, `SELECT p.viewer_class,p.coverage,
 EXTRACT(EPOCH FROM clock_timestamp()-COALESCE(p.last_progress_at,p.created_at))::float8,p.completed_at IS NOT NULL
 FROM mpd_playback_sessions p JOIN mpd_playback_facts f ON f.session_id=p.session_id
 WHERE p.session_id=$1 AND p.video_id=$2 AND p.media_version=$3 AND p.expires_at>now() FOR UPDATE OF p`,
		p.SessionID, videoID, version).Scan(&class, &coverage, &since, &completed)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, ErrPlaybackClosed
	} else if err != nil {
		return time.Time{}, err
	}
	if !published && class != "staff_preview" {
		return time.Time{}, ErrPlaybackClosed
	}

	size := (max(duration, 0) + 7) / 8
	if len(coverage) != size {
		coverage = make([]byte, size)
	}
	marked := 0
	if since*maxPace >= p.CurrentTime-p.PreviousTime {
		marked = cover(coverage, p.PreviousTime, p.CurrentTime, duration)
	} else {
		marked = cover(coverage, 0, 0, duration)
	}

	var expires time.Time
	if err = tx.QueryRow(ctx, `UPDATE mpd_playback_sessions SET coverage=$2,last_progress_at=clock_timestamp(),
 expires_at=GREATEST(expires_at,now()+$3::interval) WHERE session_id=$1 RETURNING expires_at`,
		p.SessionID, coverage, sessionLife.String()).Scan(&expires); err != nil {
		return time.Time{}, err
	}

	if !completed && class != "staff_preview" && duration > 0 && float64(marked) >= WatchedShare*float64(duration) {
		if _, err = tx.Exec(ctx, `UPDATE mpd_playback_sessions SET completed_at=clock_timestamp() WHERE session_id=$1`, p.SessionID); err != nil {
			return time.Time{}, err
		}
		if _, err = tx.Exec(ctx, `SELECT mpd_enqueue_event($1,'video.playback_completed',$2,
 jsonb_build_object('playbackSessionId',$3::uuid,'viewerClass',$4::text,'watchedSeconds',$5::float8,'durationSeconds',$6::float8),
 $3::text||':completed')`, videoID, version, p.SessionID, class, float64(marked), float64(duration)); err != nil {
			return time.Time{}, err
		}
	}
	return expires, tx.Commit(ctx)
}
