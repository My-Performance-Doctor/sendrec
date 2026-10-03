-- Until #296, a deleted video was marked purged even when its thumbnail,
-- transcript or webcam delete had failed, and the cleanup sweep never looked
-- at it again. Clearing the marker lets the sweep try every deleted video
-- once more; deleting an object that is already gone succeeds. Webcam keys
-- cleared without their object being deleted cannot be recovered this way.
UPDATE videos SET file_purged_at = NULL
WHERE status = 'deleted' AND file_purged_at IS NOT NULL;
