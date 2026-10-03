package video

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sendrec/sendrec/internal/database"
)

// retiredObjectGrace is how long a replaced original outlives the switch. The
// longest presigned download URL is one hour; a viewer who loaded the page just
// before the switch keeps range-requesting that URL until it expires.
const retiredObjectGrace = "2 hours"

// uploadURLGrace is how long after a purge its keys are deleted once more. The
// longest presigned upload URL (recording and webcam) lasts 30 minutes, so an
// upload through one issued before the deletion lands within it. #326.
const uploadURLGrace = "1 hour"

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
// consumes the attempt record for $3 and retires $2 for retiredObjectGrace,
// along with the thumbnail and transcript if update cleared them.
//
// The attempt record is the fence against the sweep. The switch locks it
// first and only applies if it still exists; the sweep claims a record, under
// the same row lock, before it deletes the object. Whichever comes second
// waits for the first and then finds the record gone: either the sweep skips
// a key the video now uses, or the switch leaves the video alone because the
// object is being deleted.
//
// The record is consumed only when the switch applied. A switch that matched
// no video leaves it in place, so the upload stays tracked even if the job
// dies before discarding it.
//
// It reports whether the row was switched.
func switchFileKey(ctx context.Context, db database.DBTX, update string, args ...any) (bool, error) {
	var switched int
	err := db.QueryRow(ctx,
		`WITH held AS (
		     SELECT key FROM retired_objects WHERE key = $3 FOR UPDATE
		 ),
		 switched AS (`+update+` AND EXISTS (SELECT 1 FROM held)
		     RETURNING old.thumbnail_key AS old_thumbnail, new.thumbnail_key AS new_thumbnail,
		               old.transcript_key AS old_transcript, new.transcript_key AS new_transcript),
		 attempt AS (
		     DELETE FROM retired_objects WHERE key = $3 AND EXISTS (SELECT 1 FROM switched)
		 ),
		 retired AS (
		     INSERT INTO retired_objects (key, delete_after)
		     SELECT k, now() + INTERVAL '`+retiredObjectGrace+`' FROM switched,
		         LATERAL (VALUES ($2::text),
		             (CASE WHEN old_thumbnail IS DISTINCT FROM new_thumbnail THEN old_thumbnail END),
		             (CASE WHEN old_transcript IS DISTINCT FROM new_transcript THEN old_transcript END)) AS v(k)
		     WHERE k IS NOT NULL
		     ON CONFLICT (key) DO UPDATE SET delete_after = EXCLUDED.delete_after
		 )
		 SELECT count(*) FROM switched`,
		args...).Scan(&switched)
	return switched > 0, err
}

// publishUpload runs update, which must set column to the upload $2 on video
// $1 and match only while the upload is still wanted. Like switchFileKey it
// locks the upload's attempt record, applies only while it exists and consumes
// it only if it applied, which fences it against the sweep, and it retires the
// value it replaces for retiredObjectGrace. It reports whether the video was updated;
// when it wasn't, the caller discards the upload.
func publishUpload(ctx context.Context, db database.DBTX, column, update string, args ...any) (bool, error) {
	var published int
	err := db.QueryRow(ctx,
		`WITH held AS (
		     SELECT key FROM retired_objects WHERE key = $2 FOR UPDATE
		 ),
		 published AS (`+update+` AND EXISTS (SELECT 1 FROM held) RETURNING old.`+column+` AS previous),
		 attempt AS (
		     DELETE FROM retired_objects WHERE key = $2 AND EXISTS (SELECT 1 FROM published)
		 ),
		 retired AS (
		     INSERT INTO retired_objects (key, delete_after)
		     SELECT previous, now() + INTERVAL '`+retiredObjectGrace+`' FROM published
		     WHERE previous IS NOT NULL AND previous <> $2
		     ON CONFLICT (key) DO UPDATE SET delete_after = EXCLUDED.delete_after
		 )
		 SELECT count(*) FROM published`,
		args...).Scan(&published)
	return published > 0, err
}

// DeleteRetiredObjects deletes objects whose delete_after has passed. A key a
// video still references is never deleted, only dropped from the table.
//
// Each key is claimed, by deleting its record under the same conditions,
// before its object is deleted, in one transaction; see deleteRetiredObject.
// A key whose storage delete fails gets a later delete_after, so it neither
// gets lost nor holds up the keys behind it.
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
		deleteRetiredObject(ctx, db, storage, key)
	}
}

// retiredObjectOpTimeout bounds each step of the cleanup transaction: the
// claim, the storage delete, the push-back and the commit. A var so tests
// can shorten it.
var retiredObjectOpTimeout = time.Minute

// deleteRetiredObject claims key and deletes its object inside one
// transaction, so the claim holds the record's row lock until the storage
// delete is done. A crash or a failed delete in between rolls the claim back
// or pushes the record behind the keys that are deletable now; either way
// the record outlives the object. A switch waiting on the lock sees the
// record gone and leaves the video alone.
func deleteRetiredObject(ctx context.Context, db database.DBTX, storage ObjectStorage, key string) {
	pool, ok := db.(interface {
		Begin(context.Context) (pgx.Tx, error)
	})
	if !ok {
		slog.Error("retired-objects: database cannot start a transaction, not sweeping", "key", key)
		return
	}
	// Every step gets a deadline of its own, so a stalled storage delete
	// can't hold the record's lock, the connection and the rest of the
	// loop. A delete that times out counts as failed and is pushed back.
	// Steps derive from ctx, so a shutdown rolls the claim back instead.
	step := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(ctx, retiredObjectOpTimeout)
	}

	beginCtx, cancel := step()
	tx, err := pool.Begin(beginCtx)
	cancel()
	if err != nil {
		slog.Error("retired-objects: failed to begin", "key", key, "error", err)
		return
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()

	claimCtx, cancel := step()
	tag, err := tx.Exec(claimCtx,
		`DELETE FROM retired_objects r
		 WHERE r.key = $1 AND r.delete_after < now() AND NOT `+referenced, key)
	cancel()
	if err != nil {
		slog.Error("retired-objects: failed to claim key", "key", key, "error", err)
		return
	}
	if tag.RowsAffected() == 0 {
		return // adopted, re-armed or claimed elsewhere since the query
	}

	deleteCtx, cancel := step()
	err = storage.DeleteObject(deleteCtx, key)
	cancel()
	if err != nil {
		slog.Error("retired-objects: failed to delete object, retrying later", "key", key, "error", err)
		pushCtx, cancel := step()
		_, err := tx.Exec(pushCtx,
			`INSERT INTO retired_objects (key, delete_after) VALUES ($1, now() + INTERVAL '`+retryDeleteAfter+`')`, key)
		cancel()
		if err != nil {
			slog.Error("retired-objects: failed to push key back", "key", key, "error", err)
			return
		}
	}

	commitCtx, cancel := step()
	defer cancel()
	if err := tx.Commit(commitCtx); err != nil {
		slog.Error("retired-objects: failed to commit", "key", key, "error", err)
	}
}
