package video

import (
	"context"
	"log/slog"

	"github.com/sendrec/sendrec/internal/database"
)

// retiredObjectGrace is how long a replaced original outlives the switch. The
// longest presigned download URL is one hour; a viewer who loaded the page just
// before the switch keeps range-requesting that URL until it expires.
const retiredObjectGrace = "2 hours"

// replacementAttemptGrace bounds an upload whose job never reaches the switch.
// It is far past every job deadline, so the sweep can't take an object a live
// job is about to switch the row to.
const replacementAttemptGrace = "1 day"

// recordReplacementAttempt notes newKey before anything is uploaded to it, so
// an upload stranded by a crash is still found and deleted.
func recordReplacementAttempt(ctx context.Context, db database.DBTX, newKey string) error {
	_, err := db.Exec(ctx,
		`INSERT INTO retired_objects (key, delete_after) VALUES ($1, now() + INTERVAL '`+replacementAttemptGrace+`')
		 ON CONFLICT (key) DO UPDATE SET delete_after = EXCLUDED.delete_after`,
		newKey)
	return err
}

// retryDeleteAfter is how long a key whose storage delete failed waits before
// the next try, so it falls behind keys that are deletable now.
const retryDeleteAfter = "1 hour"

// referenced matches a retired_objects row r whose key any video still uses,
// in any of the columns that hold object keys. Only file_key is indexed; the
// others are a scan per candidate.
// ponytail: fine at hundreds of thousands of videos; index the other three
// columns if the sweep shows up in slow-query logs.
//
// A deleted video doesn't count: its objects are being purged, and a job or
// upload that recreated one after the purge would otherwise keep it forever.
const referenced = `(EXISTS (SELECT 1 FROM videos v WHERE v.file_key = r.key AND v.status != 'deleted')
	OR EXISTS (SELECT 1 FROM videos v WHERE v.thumbnail_key = r.key AND v.status != 'deleted')
	OR EXISTS (SELECT 1 FROM videos v WHERE v.transcript_key = r.key AND v.status != 'deleted')
	OR EXISTS (SELECT 1 FROM videos v WHERE v.webcam_key = r.key AND v.status != 'deleted'))`

// discardReplacement makes an upload that will never be used due for deletion
// on the next sweep. The record may already be gone, consumed by a switch
// that matched no video, so it is written back. The sweep still skips the key
// if a video points at it, which covers a switch that landed although its
// caller saw an error.
func discardReplacement(ctx context.Context, db database.DBTX, newKey string) {
	if _, err := db.Exec(ctx,
		`INSERT INTO retired_objects (key, delete_after) VALUES ($1, now())
		 ON CONFLICT (key) DO UPDATE SET delete_after = EXCLUDED.delete_after`, newKey,
	); err != nil {
		slog.Error("retired-objects: failed to discard replacement", "key", newKey, "error", err)
	}
}

// switchFileKey runs update, which must set file_key from $2 to $3 for video $1
// and match only the row the job started from. In the same statement it
// consumes the attempt record for $3 and retires $2 for retiredObjectGrace.
//
// The attempt record is the fence against the sweep. The switch only applies
// if it could delete that record, and the sweep deletes the record before it
// deletes the object. Both take the record's row lock, so whichever comes
// second waits for the first and then finds the record gone: either the sweep
// skips a key the video now uses, or the switch leaves the video alone because
// the object is being deleted.
//
// It reports whether the row was switched. When it wasn't, the attempt record
// may have been consumed anyway; the caller discards the upload, which writes
// it back.
func switchFileKey(ctx context.Context, db database.DBTX, update string, args ...any) (bool, error) {
	var switched int
	err := db.QueryRow(ctx,
		`WITH attempt AS (
		     DELETE FROM retired_objects WHERE key = $3 RETURNING key
		 ),
		 switched AS (`+update+` AND EXISTS (SELECT 1 FROM attempt) RETURNING id),
		 retired AS (
		     INSERT INTO retired_objects (key, delete_after)
		     SELECT $2, now() + INTERVAL '`+retiredObjectGrace+`' FROM switched
		     ON CONFLICT (key) DO UPDATE SET delete_after = EXCLUDED.delete_after
		 )
		 SELECT count(*) FROM switched`,
		args...).Scan(&switched)
	return switched > 0, err
}

// publishUpload runs update, which must point video $1 at the upload $2 and
// match only while the video is live. Like switchFileKey it consumes the
// upload's attempt record in the same statement and applies only if it could,
// which fences it against the sweep. It reports whether the video was
// updated; when it wasn't, the caller discards the upload.
func publishUpload(ctx context.Context, db database.DBTX, update string, args ...any) (bool, error) {
	var published int
	err := db.QueryRow(ctx,
		`WITH attempt AS (
		     DELETE FROM retired_objects WHERE key = $2 RETURNING key
		 ),
		 published AS (`+update+` AND EXISTS (SELECT 1 FROM attempt) RETURNING id)
		 SELECT count(*) FROM published`,
		args...).Scan(&published)
	return published > 0, err
}

// DeleteRetiredObjects deletes objects whose delete_after has passed. A key a
// video still references is never deleted, only dropped from the table.
//
// Each key is claimed, by deleting its record under the same conditions,
// before its object is deleted; see switchFileKey for why. A key whose
// storage delete fails is written back with a later delete_after, so it
// neither gets lost nor holds up the keys behind it. A crash between the claim
// and the storage delete leaks that one object.
func DeleteRetiredObjects(ctx context.Context, db database.DBTX, storage ObjectStorage) {
	if _, err := db.Exec(ctx,
		`DELETE FROM retired_objects r WHERE r.delete_after < now() AND `+referenced,
	); err != nil {
		slog.Error("retired-objects: failed to drop live keys", "error", err)
		return
	}

	rows, err := db.Query(ctx,
		`SELECT r.key FROM retired_objects r
		 WHERE r.delete_after < now() AND NOT `+referenced+`
		 ORDER BY r.delete_after LIMIT 100`)
	if err != nil {
		slog.Error("retired-objects: failed to query", "error", err)
		return
	}
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			slog.Error("retired-objects: failed to scan", "error", err)
			return
		}
		keys = append(keys, key)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		slog.Error("retired-objects: row iteration error", "error", err)
		return
	}

	for _, key := range keys {
		tag, err := db.Exec(ctx,
			`DELETE FROM retired_objects r
			 WHERE r.key = $1 AND r.delete_after < now() AND NOT `+referenced, key)
		if err != nil {
			slog.Error("retired-objects: failed to claim key", "key", key, "error", err)
			continue
		}
		if tag.RowsAffected() == 0 {
			continue // adopted, re-armed or claimed elsewhere since the query
		}
		if err := storage.DeleteObject(ctx, key); err != nil {
			slog.Error("retired-objects: failed to delete object", "key", key, "error", err)
			if _, err := db.Exec(ctx,
				`INSERT INTO retired_objects (key, delete_after) VALUES ($1, now() + INTERVAL '`+retryDeleteAfter+`')
				 ON CONFLICT (key) DO NOTHING`, key,
			); err != nil {
				slog.Error("retired-objects: failed to keep key for retry", "key", key, "error", err)
			}
		}
	}
}
