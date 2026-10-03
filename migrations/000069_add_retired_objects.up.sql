-- Objects that are due for deletion but may still be needed for a while: an
-- original replaced by an edit or conversion stays until the presigned URLs
-- already handed out for it have expired, and a replacement being uploaded is
-- recorded before the upload so a crash can't orphan it. The cleanup loop
-- deletes each object once delete_after has passed, unless a video still
-- points at it.
CREATE TABLE retired_objects (
    key          TEXT PRIMARY KEY,
    delete_after TIMESTAMPTZ NOT NULL
);

CREATE INDEX idx_retired_objects_delete_after ON retired_objects (delete_after);

-- The sweep checks every candidate against videos.file_key.
CREATE INDEX idx_videos_file_key ON videos (file_key);
