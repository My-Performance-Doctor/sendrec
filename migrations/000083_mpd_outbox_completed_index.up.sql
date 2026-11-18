-- MPD-460: finds a link's earlier completion events, for the watch row's seed
-- and the check before the union sends its own. One statement, so
-- golang-migrate sends it outside a transaction and it builds without
-- blocking enqueues from the running tasks.
CREATE INDEX CONCURRENTLY IF NOT EXISTS mpd_outbox_completed ON mpd_event_outbox(video_id, media_version) WHERE event_type = 'video.playback_completed';
