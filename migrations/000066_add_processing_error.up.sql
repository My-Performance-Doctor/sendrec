-- The latest edit or conversion failure, shown to the owner on the video
-- page. NULL when the last attempt succeeded or nothing has failed.
ALTER TABLE videos ADD COLUMN processing_error TEXT;
