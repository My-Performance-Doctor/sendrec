-- When a title suggestion was last attempted, whatever the outcome. The
-- backfill tries each video once, so a failing AI call or a dismissed
-- suggestion is not retried every ten seconds.
ALTER TABLE videos ADD COLUMN title_suggested_at TIMESTAMPTZ;
