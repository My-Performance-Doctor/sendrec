-- MPD-460: verified playing time per playback session, so SendRec can tell
-- mpd-api once a session has played 80% of the video (video.playback_completed).
-- last_position and last_progress_at chain observations: only time that
-- continues from the previous one, at no more than about four times real time,
-- counts, so a seek or a replayed observation adds nothing.
ALTER TABLE mpd_playback_sessions
  ADD COLUMN played_seconds DOUBLE PRECISION NOT NULL DEFAULT 0,
  ADD COLUMN last_position DOUBLE PRECISION,
  ADD COLUMN last_progress_at TIMESTAMPTZ,
  ADD COLUMN completed_at TIMESTAMPTZ;
