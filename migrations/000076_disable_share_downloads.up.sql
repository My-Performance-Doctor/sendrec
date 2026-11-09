-- Share links are how patients watch their videos, and they must not offer a
-- download. Staff download from the library, which does not read this column.
UPDATE videos SET download_enabled = false WHERE download_enabled;
ALTER TABLE videos ALTER COLUMN download_enabled SET DEFAULT false;
ALTER TABLE videos ADD CONSTRAINT videos_share_downloads_off CHECK (NOT download_enabled);
