-- When a conversion (WebM transcode or MP4 normalize) of the video started.
-- Another conversion skips the video until the first one's deadline has
-- passed, so the sweep on each task no longer repeats an encode in progress.
ALTER TABLE videos ADD COLUMN encode_started_at TIMESTAMPTZ;
