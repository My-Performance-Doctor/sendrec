-- Counts content changes: edits bump it, conversions don't. A thumbnail or
-- transcript is only published for the version it was made from, so a job
-- that started before an edit can't overwrite what the edit produced, while
-- one that overlaps a conversion of the same content still lands.
ALTER TABLE videos ADD COLUMN media_version INTEGER NOT NULL DEFAULT 0;
