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

// discardReplacement makes an upload that will never be used due for deletion
// on the next sweep. The sweep still skips it if a video points at it, which
// covers a switch that landed although its caller saw an error.
func discardReplacement(ctx context.Context, db database.DBTX, newKey string) {
	if _, err := db.Exec(ctx,
		`UPDATE retired_objects SET delete_after = now() WHERE key = $1`, newKey,
	); err != nil {
		slog.Error("retired-objects: failed to discard replacement", "key", newKey, "error", err)
	}
}

// switchFileKey runs update, which must set file_key from $2 to $3 for video $1
// and match only the row the job started from, and in the same statement
// retires $2 for retiredObjectGrace and stops tracking $3. It reports whether
// the row was switched.
func switchFileKey(ctx context.Context, db database.DBTX, update string, args ...any) (bool, error) {
	var switched int
	err := db.QueryRow(ctx,
		`WITH switched AS (`+update+` RETURNING id),
		 adopted AS (
		     DELETE FROM retired_objects WHERE key = $3 AND EXISTS (SELECT 1 FROM switched)
		 ),
		 retired AS (
		     INSERT INTO retired_objects (key, delete_after)
		     SELECT $2, now() + INTERVAL '`+retiredObjectGrace+`' FROM switched
		     ON CONFLICT (key) DO UPDATE SET delete_after = EXCLUDED.delete_after
		 )
		 SELECT count(*) FROM switched`,
		args...).Scan(&switched)
	return switched > 0, err
}

// DeleteRetiredObjects deletes objects whose delete_after has passed. A key a
// video still points at is never deleted, only dropped from the table; a key
// whose storage delete fails stays for the next run.
func DeleteRetiredObjects(ctx context.Context, db database.DBTX, storage ObjectStorage) {
	if _, err := db.Exec(ctx,
		`DELETE FROM retired_objects r
		 WHERE r.delete_after < now()
		   AND EXISTS (SELECT 1 FROM videos v WHERE v.file_key = r.key)`,
	); err != nil {
		slog.Error("retired-objects: failed to drop live keys", "error", err)
		return
	}

	rows, err := db.Query(ctx,
		`SELECT r.key FROM retired_objects r
		 WHERE r.delete_after < now()
		   AND NOT EXISTS (SELECT 1 FROM videos v WHERE v.file_key = r.key)
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
		if err := storage.DeleteObject(ctx, key); err != nil {
			slog.Error("retired-objects: failed to delete object", "key", key, "error", err)
			continue
		}
		if _, err := db.Exec(ctx,
			`DELETE FROM retired_objects WHERE key = $1 AND delete_after < now()`, key,
		); err != nil {
			slog.Error("retired-objects: failed to forget deleted object", "key", key, "error", err)
		}
	}
}
