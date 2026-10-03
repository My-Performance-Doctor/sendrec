package video

import (
	"context"
	"log/slog"
	"time"

	"github.com/sendrec/sendrec/internal/database"
	"github.com/sendrec/sendrec/internal/webhook"
)

// stuckProcessingAfter is how long a video may sit in status='processing'
// before the owning job is presumed dead. Trim and remove-segments run under a
// 10 minute context, so anything past this bound belongs to a process that can
// no longer finish it. Composite runs longer on long recordings and gets the
// same extra allowance here as its own timeout.
const stuckProcessingAfter = "15 minutes"

// resetStuckProcessing does what the in-process setReadyFallback would have
// done for rows whose owner never got the chance.
//
// Every editing job flips the row to 'processing', works, then flips it back on
// success or via setReadyFallback on error. That covers Go error returns and
// nothing else: a SIGKILL, an OOM, an evicted pod or a node drain skips the
// fallback entirely and strands the row. Nothing else looks at 'processing'
// rows — the transcode and normalize workers both filter on 'ready' — so
// without this sweep the video shows the processing overlay forever and both
// edit endpoints reject it with 409.
//
// Resetting is safe because all three jobs upload only after ffmpeg succeeds:
// a row abandoned mid-job still has its original object at file_key and its
// original duration. The row is the only thing that is wrong.
//
// The deadline is keyed on processing_started_at rather than updated_at because
// updated_at moves for unrelated writes (a title edit, say), which would push
// the deadline out indefinitely on exactly the rows that need sweeping. A NULL
// processing_started_at means the row was stranded before this column existed,
// so those are swept on the first pass.
//
// An abandoned overlay leaves a webcam object that will never be composited.
// As in composite's own fallback, it is deleted and webcam_key cleared only
// once the object is gone, so a failed delete still leaves the key for the
// video's deletion to purge. #296.
//
// A reset composite publishes the screen recording, so the video becomes
// watchable here and ready sends video.ready. Other edits were watchable
// before they started and send nothing. BG-13.
func resetStuckProcessing(ctx context.Context, db database.DBTX, storage ObjectStorage, ready videoReadyHook) {
	// A composite's own deadline grows with the recording (compositeTimeout),
	// so rows still holding a webcam get the same allowance before they are
	// presumed dead, and the owner is told the webcam was dropped.
	rows, err := db.Query(ctx,
		`UPDATE videos SET status = 'ready', processing_started_at = NULL,
		        capture_warning = CASE
		            WHEN webcam_key IS NULL THEN capture_warning
		            WHEN capture_warning IS NULL THEN $1
		            ELSE capture_warning || ' ' || $1 END,
		        updated_at = now()
		 WHERE status = 'processing'
		   AND (processing_started_at < now() - INTERVAL '`+stuckProcessingAfter+`'
		            - CASE WHEN webcam_key IS NOT NULL THEN make_interval(secs => LEAST(2 * duration, $2)) ELSE INTERVAL '0 seconds' END
		        OR processing_started_at IS NULL)
		 RETURNING id, webcam_key, user_id, share_token, duration`,
		webcamDroppedWarning, compositeExtraCapSeconds,
	)
	if err != nil {
		slog.Error("stuck-processing: failed to reset abandoned jobs", "error", err)
		return
	}
	type composite struct {
		id, webcamKey, userID, shareToken string
		duration                          int
	}
	var composites []composite
	n := 0
	for rows.Next() {
		var c composite
		var webcamKey *string
		if err := rows.Scan(&c.id, &webcamKey, &c.userID, &c.shareToken, &c.duration); err != nil {
			slog.Error("stuck-processing: failed to scan reset row", "error", err)
			continue
		}
		n++
		if webcamKey != nil {
			c.webcamKey = *webcamKey
			composites = append(composites, c)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		slog.Error("stuck-processing: failed to reset abandoned jobs", "error", err)
		return
	}
	if n > 0 {
		slog.Warn("stuck-processing: reset abandoned jobs to ready", "count", n, "older_than", stuckProcessingAfter)
	}
	for _, c := range composites {
		dropWebcam(ctx, db, storage, c.id, c.webcamKey)
		ready.send(c.userID, c.id, c.shareToken, c.duration)
	}
}

// StartStuckProcessingWorker sweeps once at startup — the pod that died is
// usually the pod that comes back — and then on every tick, which also covers
// node-level failures where a different pod inherits the work.
func StartStuckProcessingWorker(ctx context.Context, db database.DBTX, storage ObjectStorage, hooks *webhook.Client, baseURL string, interval time.Duration) {
	ready := videoReadyHook{hooks, baseURL}
	go func() {
		resetStuckProcessing(ctx, db, storage, ready)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				slog.Info("stuck-processing: shutting down")
				return
			case <-ticker.C:
				resetStuckProcessing(ctx, db, storage, ready)
			}
		}
	}()
}
