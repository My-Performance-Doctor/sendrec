package video

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/sendrec/sendrec/internal/database"
	"github.com/sendrec/sendrec/internal/mpd"
)

// PurgeOrphanedFiles retries the storage cleanup of deleted videos whose
// objects are not all gone yet, oldest attempt first. A row that fails goes to
// the back of the queue, so a few undeletable objects cannot hold the batch
// and starve the rest.
func PurgeOrphanedFiles(ctx context.Context, db database.DBTX, storage ObjectStorage) {
	rows, err := db.Query(ctx,
		`SELECT id, file_key, thumbnail_key, webcam_key, transcript_key FROM videos
		 WHERE status = 'deleted' AND file_purged_at IS NULL
 AND (NOT EXISTS (SELECT 1 FROM mpd_video_state s WHERE s.video_id=videos.id)
 OR EXISTS (SELECT 1 FROM mpd_video_state s JOIN mpd_event_outbox e ON e.video_id=s.video_id AND e.event_type='video.deleted' WHERE s.video_id=videos.id AND s.deleted_at IS NOT NULL))
		 ORDER BY updated_at
		 LIMIT 50`)
	if err != nil {
		slog.Error("cleanup: failed to query orphaned files", "error", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var videoID, fileKey string
		var thumbnailKey, webcamKey, transcriptKey *string
		if err := rows.Scan(&videoID, &fileKey, &thumbnailKey, &webcamKey, &transcriptKey); err != nil {
			slog.Error("cleanup: failed to scan file key", "error", err)
			continue
		}
		if err := purgeVideoObjects(ctx, db, storage, videoID, &fileKey, thumbnailKey, webcamKey, transcriptKey); err != nil {
			slog.Error("cleanup: video objects not purged", "video_id", videoID, "error", err)
			if _, err := db.Exec(ctx, `UPDATE videos SET updated_at = now() WHERE id = $1`, videoID); err != nil {
				slog.Error("cleanup: failed to requeue video", "video_id", videoID, "error", err)
			}
		}
	}
	if err := rows.Err(); err != nil {
		slog.Error("cleanup: row iteration error", "error", err)
	}
}

// purgeVideoObjects deletes every object a deleted video references, then
// marks the row purged and schedules the final purge. If any delete fails the row is left unmarked, so the
// cleanup sweep tries all of them again; deleting an object that is already
// gone succeeds. Nil keys are skipped. #296.
func purgeVideoObjects(ctx context.Context, db database.DBTX, storage ObjectStorage, videoID string, keys ...*string) error {
	var errs []error
	for _, key := range keys {
		if key == nil {
			continue
		}
		if err := deleteWithRetry(ctx, storage, *key, 3); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	// An upload URL issued before the deletion can put any of these back, so
	// the same statement hands them to the retired-objects sweep for a final
	// purge once every such URL has expired. The sweep ignores deleted rows,
	// so it deletes them then. #326.
	if _, err := db.Exec(ctx,
		`WITH marked AS (
		     UPDATE videos SET file_purged_at = now() WHERE id = $1
		     RETURNING file_key, thumbnail_key, webcam_key, transcript_key
		 )
		 INSERT INTO retired_objects (key, delete_after)
		 SELECT k, now() + INTERVAL '`+uploadURLGrace+`'
		 FROM marked, unnest(ARRAY[file_key, thumbnail_key, webcam_key, transcript_key]) AS k
		 WHERE k IS NOT NULL
		 ON CONFLICT (key) DO UPDATE SET delete_after = GREATEST(retired_objects.delete_after, EXCLUDED.delete_after)`,
		videoID,
	); err != nil {
		return fmt.Errorf("mark purged: %w", err)
	}
	return nil
}

// staleUploadAgeHours is how long a video may sit in 'uploading' before it is
// considered abandoned. Finalization is a single request made right after the
// upload, so anything older than this will never complete.
const staleUploadAgeHours = 24

// AbandonStaleUploads retires videos whose finalize step never succeeded, for
// example when upload verification rejected the stored object. They would
// otherwise stay in the 'uploading' state and render as "uploading..." forever.
func AbandonStaleUploads(ctx context.Context, db database.DBTX) {
	tag, err := db.Exec(ctx,
		`UPDATE videos SET status = 'deleted', updated_at = now()
		 WHERE status = 'uploading' AND created_at < now() - make_interval(hours => $1)`,
		staleUploadAgeHours)
	if err != nil {
		slog.Error("cleanup: failed to abandon stale uploads", "error", err)
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		slog.Info("cleanup: abandoned stale uploads", "count", n)
	}
}

func StartCleanupLoop(ctx context.Context, db database.DBTX, storage ObjectStorage, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				slog.Info("cleanup: shutting down")
				return
			case <-ticker.C:
				if err := mpd.CleanupExpired(ctx, db, 10000); err != nil {
					slog.Error("cleanup: expired MPD access cleanup unavailable")
				}
				AbandonStaleUploads(ctx, db)
				PurgeOrphanedFiles(ctx, db, storage)
				DeleteRetiredObjects(ctx, db, storage)
				RegenerateMissingThumbnails(ctx, db, storage)
			}
		}
	}()
}
