package mpdevents

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/sendrec/sendrec/internal/database"
)

// Session is an unpredictable, server-issued bearer proof. Its binding and
// expiry are stored durably; callers cannot change the viewer class.
type Session struct {
	ID           string    `json:"playbackSessionId"`
	MediaVersion int       `json:"mediaVersion"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

// NewSession must be called after current publication/password or authenticated
// preview access checks. Recipient is deliberately unavailable until grant support.
func NewSession(ctx context.Context, db database.DBTX, videoID string, staffPreview bool) (Session, error) {
	class := "anonymous"
	if staffPreview {
		class = "staff_preview"
	}
	var s Session
	err := db.QueryRow(ctx, `INSERT INTO mpd_playback_sessions(video_id,media_version,viewer_class,expires_at)
 SELECT s.video_id,s.media_version,$2,now()+interval '5 minutes' FROM mpd_video_state s JOIN videos v ON v.id=s.video_id
 WHERE s.video_id=$1 AND s.deleted_at IS NULL AND v.status='ready' AND (s.published OR $3)
 RETURNING session_id,media_version,expires_at`, videoID, class, staffPreview).Scan(&s.ID, &s.MediaVersion, &s.ExpiresAt)
	return s, err
}

type Progress struct {
	SessionID           string  `json:"playbackSessionId"`
	PreviousTime        float64 `json:"previousTime"`
	CurrentTime         float64 `json:"currentTime"`
	ElapsedMilliseconds int     `json:"elapsedMilliseconds"`
	Playing             bool    `json:"playing"`
	Seeking             bool    `json:"seeking"`
}

func (p Progress) Valid() bool {
	delta := p.CurrentTime - p.PreviousTime
	return p.Playing && !p.Seeking && p.PreviousTime >= 0 && !math.IsNaN(delta) && !math.IsInf(delta, 0) && p.ElapsedMilliseconds >= 250 && p.ElapsedMilliseconds <= 10000 && delta >= 0.2 && delta <= float64(p.ElapsedMilliseconds)/1000*2.1
}

// Accept records the first advancing playback fact and outbox entry in a single
// statement. Locking the video serializes this with deletion and media edits.
func Accept(ctx context.Context, db database.DBTX, videoID string, p Progress) error {
	if !p.Valid() {
		return errors.New("advancing playback is required")
	}
	var accepted bool
	err := db.QueryRow(ctx, `WITH locked AS MATERIALIZED (
 SELECT s.video_id,s.media_version FROM videos v JOIN mpd_video_state s ON s.video_id=v.id
 WHERE v.id=$1 AND v.status='ready' AND s.deleted_at IS NULL FOR UPDATE OF v,s
 ), valid AS MATERIALIZED (
 SELECT p.* FROM mpd_playback_sessions p JOIN locked v ON v.video_id=p.video_id AND v.media_version=p.media_version
 JOIN mpd_video_state s ON s.video_id=p.video_id
 WHERE p.session_id=$2 AND p.expires_at>now() AND (s.published OR p.viewer_class='staff_preview')
 ), fact AS (
 INSERT INTO mpd_playback_facts(session_id,video_id,media_version) SELECT session_id,video_id,media_version FROM valid
 ON CONFLICT(session_id) DO NOTHING RETURNING *
 ), emitted AS (
 SELECT mpd_enqueue_event(f.video_id,'video.playback_started',f.media_version,jsonb_build_object('playbackSessionId',f.session_id,'viewerClass',v.viewer_class),f.session_id::text||':playback') FROM fact f JOIN valid v USING(session_id)
 ) SELECT EXISTS(SELECT 1 FROM valid),count(*) FROM emitted`, videoID, p.SessionID).Scan(&accepted, new(int))
	if err != nil {
		return err
	}
	if !accepted {
		return errors.New("playback session expired or media changed")
	}
	return nil
}
