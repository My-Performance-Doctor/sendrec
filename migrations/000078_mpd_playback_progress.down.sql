ALTER TABLE mpd_playback_sessions
  DROP COLUMN completed_at,
  DROP COLUMN last_progress_at,
  DROP COLUMN last_position,
  DROP COLUMN played_seconds;
