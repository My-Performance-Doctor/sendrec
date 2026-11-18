-- MPD-460: how much of a link's video its patients have watched, across every
-- play of that link, not within one sitting. `coverage` holds one bit per
-- second of the video, the union of the seconds played by its non-preview
-- playback sessions; `reported_step` is the last 5% step sent as
-- video.playback_progress; `completed_at` is when the union first reached 80%
-- and video.playback_completed was sent, once per media version.
CREATE TABLE mpd_video_watch (
 video_id UUID NOT NULL,
 media_version INTEGER NOT NULL CHECK(media_version > 0),
 coverage BYTEA,
 reported_step INTEGER NOT NULL DEFAULT 0 CHECK(reported_step BETWEEN 0 AND 20),
 completed_at TIMESTAMPTZ,
 PRIMARY KEY(video_id, media_version)
);
-- A link whose session already completed has had its completion event, so it
-- is not sent again under the across-plays rule. Earlier partial plays stay
-- with their sessions; the union starts from plays after this migration.
INSERT INTO mpd_video_watch(video_id, media_version, completed_at)
SELECT video_id, media_version, min(completed_at)
FROM mpd_playback_sessions
WHERE completed_at IS NOT NULL AND viewer_class <> 'staff_preview'
GROUP BY video_id, media_version;
