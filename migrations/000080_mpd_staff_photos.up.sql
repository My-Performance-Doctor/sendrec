-- The photo mpd-api offers beside each staff access grant. Managed watch pages
-- show it beside the staff member's name, which sign-in keeps in users.name. It
-- is small enough to inline in the page, so the page's own access check covers it.
CREATE TABLE mpd_staff_photos (
 user_id UUID PRIMARY KEY REFERENCES mpd_external_identities(user_id) ON DELETE CASCADE,
 version TEXT NOT NULL,
 jpeg BYTEA NOT NULL CHECK (octet_length(jpeg) <= 65536),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
