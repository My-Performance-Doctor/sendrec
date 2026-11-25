-- SNDR-1: one row per Vimeo video imported into the MPD workspace, so a rerun of the
-- operator import never creates a second video for the same Vimeo id.
CREATE TABLE mpd_vimeo_imports (
    vimeo_video_id TEXT PRIMARY KEY CHECK (vimeo_video_id ~ '^[0-9]{1,20}$'),
    video_id UUID NOT NULL UNIQUE REFERENCES videos(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
