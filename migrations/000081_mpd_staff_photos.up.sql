-- The photo mpd-api offers beside each staff access grant. Managed watch pages
-- show it beside the staff member's name, which sign-in keeps in users.name. It
-- is small enough to inline in the page, so the page's own access check covers it.
-- want_version is the version the latest grant names, kept here so every task
-- agrees on it. version and jpeg are the photo stored for it, written only
-- while want_version still names that version.
CREATE TABLE mpd_staff_photos (
 user_id UUID PRIMARY KEY REFERENCES mpd_external_identities(user_id) ON DELETE CASCADE,
 want_version TEXT,
 version TEXT,
 jpeg BYTEA CHECK (octet_length(jpeg) <= 65536),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 CHECK ((version IS NULL) = (jpeg IS NULL))
);
