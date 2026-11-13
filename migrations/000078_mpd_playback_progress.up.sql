-- MPD-460: which seconds of the video each playback session has played, so
-- SendRec can tell mpd-api once a session has played 80% of the video
-- (video.playback_completed). `coverage` holds one bit per second of the video;
-- rewatching, replaying an observation or seeking past a stretch adds nothing.
-- `last_progress_at` paces observations against the server's clock.
ALTER TABLE mpd_playback_sessions
  ADD COLUMN coverage BYTEA,
  ADD COLUMN last_progress_at TIMESTAMPTZ,
  ADD COLUMN completed_at TIMESTAMPTZ;
