ALTER TABLE videos DROP COLUMN IF EXISTS thumbnail_retry_at;
ALTER TABLE videos DROP COLUMN IF EXISTS thumbnail_attempts;
ALTER TABLE videos DROP COLUMN IF EXISTS thumbnail_version;
ALTER TABLE videos DROP COLUMN IF EXISTS media_version;
