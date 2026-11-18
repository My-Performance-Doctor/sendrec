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
// completed, so the player stops reporting, once the link need hear no more
// (linkWatch.stops); after that a report only keeps the session open.
//
// A non-preview session's coverage also joins its link's coverage, the union
// across every play of that media version (mpd_video_watch), under a pace
// bound of its own. When the union first reaches WatchedShare it enqueues
// video.playback_completed, once per media version. With watchShare on it also
// enqueues video.playback_progress for the highest new 5% step the union has
// reached, skipping any steps between, and both events carry
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

	// A shared lock is enough to fence deletion and media changes. Viewers of
	// the same video wait on each other only for the link's row, and only when
	// a report can change it.
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

	// Once the link need hear no more, a report only keeps the session open.
	// That is read without the link's lock: completion and seeding never
	// undo, so most viewers of a completed link never wait on each other.
	if counts {
		link, found, err := readWatch(ctx, tx, videoID, version, duration, false)
		if err != nil {
			return Progressed{}, err
		}
		if found && link.stops(watchShare, duration) {
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
	// The link's row is locked, and made if missing, before this session
	// records its own completion: the row's seed reads completed sessions.
	link, err := lockWatch(ctx, tx, videoID, version, duration, age)
	if err != nil {
		return Progressed{}, err
	}
	if !completed && float64(played) >= WatchedShare*float64(duration) {
		// The session's own record that it played 80% itself.
		if _, err = tx.Exec(ctx, `UPDATE mpd_playback_sessions SET completed_at=clock_timestamp() WHERE session_id=$1`, p.SessionID); err != nil {
			return Progressed{}, err
		}
	}
	if link, err = watched(ctx, tx, videoID, version, duration, link, coverage, age, p.SessionID, class, watchShare); err != nil {
		return Progressed{}, err
	}
	return Progressed{ExpiresAt: expires, Completed: link.stops(watchShare, duration)}, tx.Commit(ctx)
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

// linkWatch is a link's row in mpd_video_watch: the union of its plays'
// seconds, sized to the video, the last step reported, whether it has
// completed or was seeded as completed, and the seconds since its union last
// grew.
type linkWatch struct {
	coverage  []byte
	seconds   int
	step      int
	completed bool
	seeded    bool
	idle      float64
}

// unionAllowance is the seconds a report may add to a link's union beyond its
// pace, as a session's bound allows one second over its own. Tests that run
// plays concurrently without a real clock raise it.
var unionAllowance = 1.0

// stops says whether the link's sessions need report no more: it has
// completed, and either the setting is off, it was seeded (its union holds
// only plays since migration 000082, so no share is reported for it), or its
// union has reached the last 5% step below the whole video. Waiting for every
// second would keep later plays reporting for one missed second.
func (w linkWatch) stops(watchShare bool, duration int) bool {
	return w.completed && (!watchShare || w.seeded || w.seconds*shareSteps >= (shareSteps-1)*duration)
}

const watchColumns = `coverage,reported_step,completed_at IS NOT NULL,seeded,EXTRACT(EPOCH FROM clock_timestamp()-grown_at)::float8`

// readWatch reads a link's row, locking it when lock is set.
func readWatch(ctx context.Context, tx pgx.Tx, videoID string, version, duration int, lock bool) (linkWatch, bool, error) {
	query := `SELECT ` + watchColumns + ` FROM mpd_video_watch WHERE video_id=$1 AND media_version=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	var w linkWatch
	err := tx.QueryRow(ctx, query, videoID, version).Scan(&w.coverage, &w.step, &w.completed, &w.seeded, &w.idle)
	if errors.Is(err, pgx.ErrNoRows) {
		return w, false, nil
	} else if err != nil {
		return w, false, err
	}
	w.coverage = fit(w.coverage, duration)
	w.seconds = marked(w.coverage)
	return w, true, nil
}

// lockWatch locks a link's row, creating it on first use as if it last grew
// when the reporting session's playback started, age seconds ago. A link with a completion already on record,
// in the outbox (under either rule) or on a completed session not yet cleaned
// up, starts completed and seeded at completedStep, so it is not completed
// twice. The row lock makes concurrent plays join in turn.
func lockWatch(ctx context.Context, tx pgx.Tx, videoID string, version, duration int, age float64) (linkWatch, error) {
	w, found, err := readWatch(ctx, tx, videoID, version, duration, true)
	if err != nil || found {
		return w, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO mpd_video_watch(video_id,media_version,completed_at,reported_step,seeded,grown_at)
 SELECT $1::uuid,$2::int,seed.at,CASE WHEN seed.at IS NULL THEN 0 ELSE $3::int END,seed.at IS NOT NULL,clock_timestamp()-make_interval(secs=>$4::float8) FROM (SELECT LEAST(
 (SELECT min(created_at) FROM mpd_event_outbox WHERE video_id=$1::uuid AND media_version=$2::int AND event_type='video.playback_completed'),
 (SELECT min(completed_at) FROM mpd_playback_sessions WHERE video_id=$1::uuid AND media_version=$2::int AND completed_at IS NOT NULL AND viewer_class<>'staff_preview')) AS at) seed
 ON CONFLICT DO NOTHING`, videoID, version, completedStep, age); err != nil {
		return w, err
	}
	w, _, err = readWatch(ctx, tx, videoID, version, duration, true)
	return w, err
}

// watched joins a session's coverage, sized to the video, to its link's
// locked row, and sends the events the union now calls for.
//
// The union grows at a pace of its own: a report adds at most maxPace times
// the seconds since the union last grew, but no more than since the reporting
// session's playback started, plus unionAllowance per report. The allowance
// is per report, so parallel sessions on disjoint stretches can fill the union
// faster than one viewer: two tabs reporting often reach 80% about twice as
// fast, and more sessions or IPs faster still. How long ago the link was
// first played no longer matters. Seconds over that budget wait, earliest
// first; the session's later reports bring them in.
func watched(ctx context.Context, tx pgx.Tx, videoID string, version, duration int, link linkWatch, session []byte, age float64, sessionID, class string, watchShare bool) (linkWatch, error) {
	budget := int(maxPace*min(link.idle, age) + unionAllowance)
	union := append([]byte(nil), link.coverage...)
	for second := 0; second < duration && budget > 0; second++ {
		bit := byte(1) << (second % 8)
		if session[second/8]&bit != 0 && union[second/8]&bit == 0 {
			union[second/8] |= bit
			budget--
		}
	}
	seconds := marked(union)
	grew := seconds > link.seconds
	rounded := math.Round(float64(seconds)/float64(duration)*10000) / 10000
	// Only the highest step reached is sent; steps between are skipped.
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
 completed_at=CASE WHEN $5 THEN clock_timestamp() ELSE completed_at END,
 grown_at=CASE WHEN $6 THEN clock_timestamp() ELSE grown_at END WHERE video_id=$1 AND media_version=$2`,
		videoID, version, union, step, complete, grew); err != nil {
		return link, err
	}
	idle := link.idle
	if grew {
		idle = 0
	}
	return linkWatch{coverage: union, seconds: seconds, step: step, completed: link.completed || complete, seeded: link.seeded, idle: idle}, nil
}
