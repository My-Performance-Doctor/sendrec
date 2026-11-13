package mpdevents

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sendrec/sendrec/internal/database"
)

type beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// WatchedShare is the share of a video one playback session must play, as
// verified playing time, for MPD to count the video as watched (MPD-460).
const WatchedShare = 0.8

// sessionLife is how long a playback session stays open after its last
// accepted observation, so a session lasts as long as the viewer keeps playing.
const sessionLife = 5 * time.Minute

// ErrPlaybackClosed means the session expired, the media changed or the
// video stopped being playable.
var ErrPlaybackClosed = errors.New("playback session expired or media changed")

// Played adds one progress observation to the session's verified playing time
// and keeps the session open. Time counts only when the observation continues
// from the session's last position, within a second, and does not run more than
// about four times faster than the server's own clock since the last one, so a
// seek, a replayed observation or a burst adds nothing and only re-anchors the
// chain. Once a non-preview session's playing time reaches WatchedShare of the
// video's length it enqueues video.playback_completed, once per session.
// It returns when the session now expires.
func Played(ctx context.Context, db database.DBTX, videoID string, p Progress) (time.Time, error) {
	if !p.Valid() {
		return time.Time{}, errors.New("advancing playback is required")
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

	var version, duration int
	var published bool
	err = tx.QueryRow(ctx, `SELECT s.media_version,v.duration,s.published FROM videos v JOIN mpd_video_state s ON s.video_id=v.id
 WHERE v.id=$1 AND v.status='ready' AND s.deleted_at IS NULL FOR UPDATE OF v,s`, videoID).Scan(&version, &duration, &published)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, ErrPlaybackClosed
	} else if err != nil {
		return time.Time{}, err
	}

	var class string
	var played float64
	var last *float64
	var since *float64
	var completed bool
	err = tx.QueryRow(ctx, `SELECT viewer_class,played_seconds,last_position,
 EXTRACT(EPOCH FROM clock_timestamp()-last_progress_at),completed_at IS NOT NULL
 FROM mpd_playback_sessions WHERE session_id=$1 AND video_id=$2 AND media_version=$3 AND expires_at>now() FOR UPDATE`,
		p.SessionID, videoID, version).Scan(&class, &played, &last, &since, &completed)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, ErrPlaybackClosed
	} else if err != nil {
		return time.Time{}, err
	}
	if !published && class != "staff_preview" {
		return time.Time{}, ErrPlaybackClosed
	}

	delta := p.CurrentTime - p.PreviousTime
	continues := last == nil || math.Abs(p.PreviousTime-*last) <= 1
	paced := since == nil || *since*4 >= delta
	if continues && paced {
		played += delta
	}

	var expires time.Time
	if err = tx.QueryRow(ctx, `UPDATE mpd_playback_sessions SET played_seconds=$2,last_position=$3,last_progress_at=clock_timestamp(),
 expires_at=GREATEST(expires_at,now()+$4::interval) WHERE session_id=$1 RETURNING expires_at`,
		p.SessionID, played, p.CurrentTime, sessionLife.String()).Scan(&expires); err != nil {
		return time.Time{}, err
	}

	if !completed && class != "staff_preview" && duration > 0 && played >= WatchedShare*float64(duration) {
		if _, err = tx.Exec(ctx, `UPDATE mpd_playback_sessions SET completed_at=clock_timestamp() WHERE session_id=$1`, p.SessionID); err != nil {
			return time.Time{}, err
		}
		if _, err = tx.Exec(ctx, `SELECT mpd_enqueue_event($1,'video.playback_completed',$2,
 jsonb_build_object('playbackSessionId',$3::uuid,'viewerClass',$4::text,'watchedSeconds',$5::float8,'durationSeconds',$6::float8),
 $3::text||':completed')`, videoID, version, p.SessionID, class, played, float64(duration)); err != nil {
			return time.Time{}, err
		}
	}
	return expires, tx.Commit(ctx)
}
