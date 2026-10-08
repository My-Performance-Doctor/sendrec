-- Receipts contain response metadata, never passwords or bearer credentials.
CREATE TABLE mpd_adapter_receipts (
 video_id UUID NOT NULL,
 operation TEXT NOT NULL CHECK(operation IN ('password','publication')),
 effect_key TEXT NOT NULL CHECK(length(effect_key) BETWEEN 1 AND 200),
 request_hash TEXT NOT NULL,
 response JSONB NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(video_id,operation,effect_key)
);
CREATE TABLE mpd_preview_handoffs (
 token_hash TEXT PRIMARY KEY,
 video_id UUID NOT NULL,
 media_version INTEGER NOT NULL,
 session_id TEXT NOT NULL,
 origin TEXT NOT NULL,
 nonce TEXT NOT NULL,
 expires_at TIMESTAMPTZ NOT NULL,
 used_at TIMESTAMPTZ
);
CREATE TABLE mpd_preview_sessions (
 token_hash TEXT PRIMARY KEY,
 video_id UUID NOT NULL,
 media_version INTEGER NOT NULL,
 session_id TEXT NOT NULL,
 expires_at TIMESTAMPTZ NOT NULL
);
