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
// loses nothing. Progress keeps the session open for sessionLife. It reports
// completed, so the player stops reporting, once the link has completed and
// either watchShare is off or the link's union holds the whole video; with
// watchShare off, a session that has covered WatchedShare itself only stays
// open after that.
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

	// A length corrected since the session began keeps the seconds already
	// played that the video still has.
	coverage = fit(coverage, duration)
	counts := class != "staff_preview" && duration > 0

	var link linkWatch
	if counts {
		if link, err = lockWatch(ctx, tx, videoID, version, duration); err != nil {
			return Progressed{}, err
		}
		// Nothing a further report adds can be sent once the link has
		// completed, unless the setting is on and its union can still grow.
		// A link seeded as completed holds only plays since migration 000082,
		// so its share is not reported: its sessions stop too.
		seeded := link.seconds < int(math.Ceil(WatchedShare*float64(duration)))
		if link.completed && (!watchShare || seeded || link.seconds >= duration) {
			var expires time.Time
			if err = tx.QueryRow(ctx, `UPDATE mpd_playback_sessions SET expires_at=GREATEST(expires_at,now()+$2::interval)
 WHERE session_id=$1 RETURNING expires_at`, p.SessionID, sessionLife.String()).Scan(&expires); err != nil {
				return Progressed{}, err
			}
			return Progressed{ExpiresAt: expires, Completed: true}, tx.Commit(ctx)
		}
	}

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
	if !counts {
		return Progressed{ExpiresAt: expires}, tx.Commit(ctx)
	}
	if !completed && float64(played) >= WatchedShare*float64(duration) {
		// The session's own record that it played 80% itself.
		if _, err = tx.Exec(ctx, `UPDATE mpd_playback_sessions SET completed_at=clock_timestamp() WHERE session_id=$1`, p.SessionID); err != nil {
			return Progressed{}, err
		}
	}
	if link, err = watched(ctx, tx, videoID, version, duration, link, coverage, p.SessionID, class, watchShare); err != nil {
		return Progressed{}, err
	}
	// With the setting on the player keeps reporting until the link's union
	// is whole, so shares above 80% still reach mpd-api.
	done := link.completed && (!watchShare || link.seconds >= duration)
	return Progressed{ExpiresAt: expires, Completed: done}, tx.Commit(ctx)
}

// fit sizes a bitmap to a video's length, keeping the seconds it still has.
func fit(bitmap []byte, duration int) []byte {
	sized := make([]byte, (max(duration, 0)+7)/8)
	copy(sized, bitmap)
	if duration%8 != 0 && len(sized) > 0 {
		sized[len(sized)-1] &= byte(1<<(duration%8)) - 1
	}
	return sized
}

// completedStep is the 5% step at WatchedShare. A link seeded as completed
// starts there, whatever the setting, so its share never reads below 80%.
const completedStep = 16

// linkWatch is a link's row in mpd_video_watch: the union of its plays' seconds,
// sized to the video, the last step reported, and whether it has completed.
type linkWatch struct {
	coverage  []byte
	seconds   int
	step      int
	completed bool
}

// lockWatch locks a link's row, creating it on first use. A link with a
// completion already on record, in the outbox (under either rule) or on a
// completed session not yet cleaned up, starts completed at completedStep, so
// it is not completed twice. The row lock makes concurrent plays join in turn.
func lockWatch(ctx context.Context, tx pgx.Tx, videoID string, version, duration int) (linkWatch, error) {
	const read = `SELECT coverage,reported_step,completed_at IS NOT NULL FROM mpd_video_watch WHERE video_id=$1 AND media_version=$2 FOR UPDATE`
	var w linkWatch
	err := tx.QueryRow(ctx, read, videoID, version).Scan(&w.coverage, &w.step, &w.completed)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err = tx.Exec(ctx, `INSERT INTO mpd_video_watch(video_id,media_version,completed_at,reported_step)
 SELECT $1::uuid,$2::int,seed.at,CASE WHEN seed.at IS NULL THEN 0 ELSE $3::int END FROM (SELECT LEAST(
 (SELECT min(created_at) FROM mpd_event_outbox WHERE video_id=$1::uuid AND media_version=$2::int AND event_type='video.playback_completed'),
 (SELECT min(completed_at) FROM mpd_playback_sessions WHERE video_id=$1::uuid AND media_version=$2::int AND completed_at IS NOT NULL AND viewer_class<>'staff_preview')) AS at) seed
 ON CONFLICT DO NOTHING`, videoID, version, completedStep); err != nil {
			return w, err
		}
		err = tx.QueryRow(ctx, read, videoID, version).Scan(&w.coverage, &w.step, &w.completed)
	}
	if err != nil {
		return w, err
	}
	w.coverage = fit(w.coverage, duration)
	w.seconds = marked(w.coverage)
	return w, nil
}

// watched joins a session's coverage, sized to the video, to its link's
// locked row, and sends the events the union now calls for.
func watched(ctx context.Context, tx pgx.Tx, videoID string, version, duration int, link linkWatch, session []byte, sessionID, class string, watchShare bool) (linkWatch, error) {
	union := append([]byte(nil), link.coverage...)
	for i := range union {
		union[i] |= session[i]
	}
	seconds := marked(union)
	rounded := math.Round(float64(seconds)/float64(duration)*10000) / 10000
	reached := min(seconds*shareSteps/duration, shareSteps)

	step := link.step
	stepped := watchShare && reached > step
	complete := !link.completed && float64(seconds) >= WatchedShare*float64(duration)
	if !stepped && !complete && seconds == link.seconds {
		return link, nil
	}
	if stepped {
		// Keys start with "watch:" so they meet no earlier per-session key.
		if _, err := tx.Exec(ctx, `SELECT mpd_enqueue_event($1::uuid,'video.playback_progress',$2::int,
 jsonb_build_object('watchedSeconds',$3::float8,'durationSeconds',$4::float8,'watchedShare',$5::float8),
 'watch:'||$1::uuid::text||':'||$2::int::text||':progress:'||$6::int::text)`, videoID, version, float64(seconds), float64(duration), rounded, reached); err != nil {
			return link, err
		}
		step = reached
	}
	if complete {
		// A task still running the per-session rule may have completed this
		// link since its row was made; then the completion is on record.
		var recorded bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mpd_event_outbox WHERE video_id=$1 AND media_version=$2 AND event_type='video.playback_completed')`,
			videoID, version).Scan(&recorded); err != nil {
			return link, err
		}
		if !recorded {
			base := `jsonb_build_object('playbackSessionId',$3::uuid,'viewerClass',$4::text,'watchedSeconds',$5::float8,'durationSeconds',$6::float8)`
			args := []any{videoID, version, sessionID, class, float64(seconds), float64(duration)}
			if watchShare {
				base += `||jsonb_build_object('watchedShare',$7::float8)`
				args = append(args, rounded)
			}
			if _, err := tx.Exec(ctx, `SELECT mpd_enqueue_event($1::uuid,'video.playback_completed',$2::int,`+base+`,'watch:'||$1::uuid::text||':'||$2::int::text||':completed')`, args...); err != nil {
				return link, err
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE mpd_video_watch SET coverage=$3,reported_step=$4,
 completed_at=CASE WHEN $5 THEN clock_timestamp() ELSE completed_at END WHERE video_id=$1 AND media_version=$2`,
		videoID, version, union, step, complete); err != nil {
		return link, err
	}
	return linkWatch{coverage: union, seconds: seconds, step: step, completed: link.completed || complete}, nil
}
