-- Which recordings had downloads on before is not kept.
ALTER TABLE videos DROP CONSTRAINT videos_share_downloads_off;
ALTER TABLE videos ALTER COLUMN download_enabled SET DEFAULT true;
