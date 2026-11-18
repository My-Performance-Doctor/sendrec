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

// WatchedShare is the share of a video its link's patients must have played,
// across every play of the link, for MPD to count the video as watched
// (MPD-460). A single session that plays it also stops reporting.
const WatchedShare = 0.8

// shareSteps divides a video into 5% steps for video.playback_progress.
const shareSteps = 20

// sessionLife is how long a playback session stays open after its last
// accepted progress, so a viewer can pause and come back without losing it.
const sessionLife = 30 * time.Minute

// maxPace bounds a session's coverage: it may hold no more seconds than this
// many times the seconds since its playback started, plus one.
const maxPace = 4

// ErrPlaybackClosed means the session expired, never started, the media
// changed or the video stopped being playable.
var ErrPlaybackClosed = errors.New("playback session expired or media changed")

// Progressed is when the session now expires, and whether it has completed,
// after which the player need not report any more.
type Progressed struct {
	ExpiresAt time.Time `json:"expiresAt"`
	Completed bool      `json:"completed"`
}

// ErrInvalidProgress means the observation was not advancing playback.
var ErrInvalidProgress = errors.New("advancing playback is required")

type beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// cover marks the seconds of the video whose midpoint lies between `from` and
// `to`, in a bitmap of one bit per second, so back-to-back stretches mark each
// second once whatever their fractions.
func cover(bitmap []byte, from, to float64, duration int) {
	first := max(0, int(math.Ceil(from-0.5)))
	last := min(duration, int(math.Ceil(to-0.5)))
	for second := first; second < last; second++ {
		bitmap[second/8] |= 1 << (second % 8)
	}
}

// marked counts the seconds a bitmap holds.
func marked(bitmap []byte) int {
	n := 0
	for _, b := range bitmap {
		n += bits.OnesCount8(b)
	}
	return n
}

// Played records one progress observation of a started, open session: the
// seconds it covers, within the video's length, join the session's coverage.
// Seconds already covered, and any stretch skipped by a seek, add nothing. An
// observation that would leave the session holding more than maxPace times
// the seconds since its playback started adds nothing, so neither bursts nor
// a late report can claim play faster than that overall, and network jitter
// loses nothing. Progress keeps the session open for sessionLife; once the
// session itself has covered WatchedShare it only does that, and reports
// completed, so the player stops reporting.
//
// A non-preview session's coverage also joins its link's coverage, the union
// across every play of that media version (mpd_video_watch). When the union
// first reaches WatchedShare it enqueues video.playback_completed, once per
// media version. With watchShare on it also enqueues video.playback_progress
// each time the union passes a new 5% step, and both events carry
// watchedShare; production mpd-api before MPD-460's change refuses those, so
// the deploy setting mpdWatchShare keeps them off until it accepts them.
func Played(ctx context.Context, db database.DBTX, videoID string, p Progress, watchShare bool) (Progressed, error) {
	if !p.Valid() {
		return Progressed{}, ErrInvalidProgress
	}
	b, ok := db.(beginner)
	if !ok {
		return Progressed{}, errors.New("playback progress needs a transaction")
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return Progressed{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// A shared lock is enough to fence deletion and media changes, and lets
	// viewers of the same video report at once.
	var version, duration int
	var published bool
	err = tx.QueryRow(ctx, `SELECT s.media_version,v.duration,s.published FROM videos v JOIN mpd_video_state s ON s.video_id=v.id
 WHERE v.id=$1 AND v.status='ready' AND s.deleted_at IS NULL FOR SHARE OF v,s`, videoID).Scan(&version, &duration, &published)
	if errors.Is(err, pgx.ErrNoRows) {
		return Progressed{}, ErrPlaybackClosed
	} else if err != nil {
		return Progressed{}, err
	}

	var class string
	var coverage []byte
	var age float64
	var completed bool
	err = tx.QueryRow(ctx, `SELECT p.viewer_class,p.coverage,
 EXTRACT(EPOCH FROM clock_timestamp()-f.occurred_at)::float8,p.completed_at IS NOT NULL
 FROM mpd_playback_sessions p JOIN mpd_playback_facts f ON f.session_id=p.session_id
 WHERE p.session_id=$1 AND p.video_id=$2 AND p.media_version=$3 AND p.expires_at>now() FOR UPDATE OF p`,
		p.SessionID, videoID, version).Scan(&class, &coverage, &age, &completed)
	if errors.Is(err, pgx.ErrNoRows) {
		return Progressed{}, ErrPlaybackClosed
	} else if err != nil {
		return Progressed{}, err
	}
	if !published && class != "staff_preview" {
		return Progressed{}, ErrPlaybackClosed
	}

	if completed {
		var expires time.Time
		if err = tx.QueryRow(ctx, `UPDATE mpd_playback_sessions SET expires_at=GREATEST(expires_at,now()+$2::interval)
 WHERE session_id=$1 RETURNING expires_at`, p.SessionID, sessionLife.String()).Scan(&expires); err != nil {
			return Progressed{}, err
		}
		return Progressed{ExpiresAt: expires, Completed: true}, tx.Commit(ctx)
	}

	// A length corrected since the session began keeps the seconds already
	// played that the video still has.
	sized := make([]byte, (max(duration, 0)+7)/8)
	copy(sized, coverage)
	if duration%8 != 0 && len(sized) > 0 {
		sized[len(sized)-1] &= byte(1<<(duration%8)) - 1
	}
	coverage = sized
	next := append([]byte(nil), coverage...)
	cover(next, p.PreviousTime, p.CurrentTime, duration)
	if float64(marked(next)) <= maxPace*age+1 {
		coverage = next
	}
	played := marked(coverage)

	var expires time.Time
	if err = tx.QueryRow(ctx, `UPDATE mpd_playback_sessions SET coverage=$2,
 expires_at=GREATEST(expires_at,now()+$3::interval) WHERE session_id=$1 RETURNING expires_at`,
		p.SessionID, coverage, sessionLife.String()).Scan(&expires); err != nil {
		return Progressed{}, err
	}

	done := class != "staff_preview" && duration > 0 && float64(played) >= WatchedShare*float64(duration)
	if done {
		if _, err = tx.Exec(ctx, `UPDATE mpd_playback_sessions SET completed_at=clock_timestamp() WHERE session_id=$1`, p.SessionID); err != nil {
			return Progressed{}, err
		}
	}
	if class != "staff_preview" && duration > 0 {
		if err = watched(ctx, tx, videoID, version, duration, coverage, p.SessionID, class, watchShare); err != nil {
			return Progressed{}, err
		}
	}
	return Progressed{ExpiresAt: expires, Completed: done}, tx.Commit(ctx)
}

// watched joins a session's coverage to its link's coverage across every play
// of the media version, and sends the events the union now calls for.
func watched(ctx context.Context, tx pgx.Tx, videoID string, version, duration int, session []byte, sessionID, class string, watchShare bool) error {
	if _, err := tx.Exec(ctx, `INSERT INTO mpd_video_watch(video_id,media_version) VALUES($1,$2) ON CONFLICT DO NOTHING`, videoID, version); err != nil {
		return err
	}
	var union []byte
	var step int
	var completed bool
	if err := tx.QueryRow(ctx, `SELECT coverage,reported_step,completed_at IS NOT NULL FROM mpd_video_watch
 WHERE video_id=$1 AND media_version=$2 FOR UPDATE`, videoID, version).Scan(&union, &step, &completed); err != nil {
		return err
	}
	sized := make([]byte, (duration+7)/8)
	copy(sized, union)
	for i := range sized {
		if i < len(session) {
			sized[i] |= session[i]
		}
	}
	if duration%8 != 0 {
		sized[len(sized)-1] &= byte(1<<(duration%8)) - 1
	}
	union = sized
	seconds := marked(union)
	share := float64(seconds) / float64(duration)
	rounded := math.Round(share*10000) / 10000
	reached := min(int(math.Floor(share*shareSteps+1e-9)), shareSteps)

	if watchShare && reached > step {
		if _, err := tx.Exec(ctx, `SELECT mpd_enqueue_event($1::uuid,'video.playback_progress',$2::int,
 jsonb_build_object('watchedSeconds',$3::float8,'durationSeconds',$4::float8,'watchedShare',$5::float8),
 $1::uuid::text||':progress:'||$2::int::text||':'||$6::int::text)`, videoID, version, float64(seconds), float64(duration), rounded, reached); err != nil {
			return err
		}
		step = reached
	}
	complete := !completed && float64(seconds) >= WatchedShare*float64(duration)
	if complete {
		base := `jsonb_build_object('playbackSessionId',$3::uuid,'viewerClass',$4::text,'watchedSeconds',$5::float8,'durationSeconds',$6::float8)`
		args := []any{videoID, version, sessionID, class, float64(seconds), float64(duration)}
		if watchShare {
			base += `||jsonb_build_object('watchedShare',$7::float8)`
			args = append(args, rounded)
		}
		if _, err := tx.Exec(ctx, `SELECT mpd_enqueue_event($1::uuid,'video.playback_completed',$2::int,`+base+`,$1::uuid::text||':completed:'||$2::int::text)`, args...); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `UPDATE mpd_video_watch SET coverage=$3,reported_step=$4,
 completed_at=CASE WHEN $5 THEN clock_timestamp() ELSE completed_at END WHERE video_id=$1 AND media_version=$2`,
		videoID, version, union, step, complete)
	return err
}
