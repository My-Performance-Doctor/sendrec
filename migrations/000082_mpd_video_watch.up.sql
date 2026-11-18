-- MPD-460: how much of a link's video its patients have watched, across every
-- play of that link, not within one sitting. `coverage` holds one bit per
-- second of the video, the union of the seconds played by its non-preview
-- playback sessions; `reported_step` is the last 5% step sent as
-- video.playback_progress; `completed_at` is when the union first reached 80%
-- and video.playback_completed was sent, once per media version.
--
-- Rows are created on a link's first progress report, not backfilled here.
-- A link with a completion already on record, in the outbox or on a session
-- not yet cleaned up, starts completed at the 80% step (mpdevents.watched).
-- Earlier partial plays stay with their sessions; the union starts from plays
-- after this migration.
CREATE TABLE mpd_video_watch (
 video_id UUID NOT NULL,
 media_version INTEGER NOT NULL CHECK(media_version > 0),
 coverage BYTEA,
 reported_step INTEGER NOT NULL DEFAULT 0 CHECK(reported_step BETWEEN 0 AND 20),
 completed_at TIMESTAMPTZ,
 PRIMARY KEY(video_id, media_version)
);
-- The seed's indexes come in 000083 and 000084, each built concurrently in a
-- file of its own so the running tasks can keep writing.
