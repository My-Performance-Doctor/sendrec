-- Wrong share-password attempts per video, counted in a fixed window, so a
-- shared link cannot be guessed by spreading attempts across addresses.
CREATE TABLE share_password_attempts (
 video_id UUID PRIMARY KEY REFERENCES videos(id) ON DELETE CASCADE,
 window_start TIMESTAMPTZ NOT NULL,
 failures INTEGER NOT NULL CHECK (failures > 0)
);
