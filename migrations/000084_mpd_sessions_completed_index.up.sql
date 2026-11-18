-- MPD-460: finds a link's completed sessions for the watch row's seed. One
-- statement, built concurrently, as in 000083.
CREATE INDEX CONCURRENTLY IF NOT EXISTS mpd_sessions_completed ON mpd_playback_sessions(video_id, media_version) WHERE completed_at IS NOT NULL;
