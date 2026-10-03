package video

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sendrec/sendrec/internal/database"
)

// transcriptionJobTimeout bounds one transcription job, download to upload,
// so a hung whisper-cli or ffmpeg cannot hold the queue. Long recordings on a
// small CPU are the slow case. A var so tests can shorten it.
var transcriptionJobTimeout = 30 * time.Minute

func EnqueueTranscription(ctx context.Context, db database.DBTX, videoID string) error {
	if !isTranscriptionEnabled() {
		return nil
	}
	_, err := db.Exec(ctx,
		`UPDATE videos SET transcript_status = 'pending', updated_at = now()
		 WHERE id = $1 AND status != 'deleted'`,
		videoID,
	)
	return err
}

func processNextTranscription(ctx context.Context, db database.DBTX, storage ObjectStorage, transcriber Transcriber, aiEnabled bool) {
	// Requeue jobs whose worker died. A live job gives up at its deadline, so
	// anything a minute past it belongs to nobody.
	if _, err := db.Exec(ctx,
		`UPDATE videos SET transcript_status = 'pending', transcript_started_at = NULL, updated_at = now()
		 WHERE transcript_status = 'processing'
		   AND (transcript_started_at < now() - make_interval(secs => $1) OR transcript_started_at IS NULL)`,
		(transcriptionJobTimeout + time.Minute).Seconds(),
	); err != nil {
		slog.Error("transcribe-worker: failed to reset stuck jobs", "error", err)
	}

	// Claim the next pending job
	var videoID, fileKey, userID, shareToken, language string
	var version int
	err := db.QueryRow(ctx,
		`UPDATE videos SET transcript_status = 'processing', transcript_started_at = now(), updated_at = now()
		 WHERE id = (
		     SELECT v.id FROM videos v
		     WHERE v.transcript_status = 'pending' AND v.status != 'deleted'
		     ORDER BY v.updated_at ASC LIMIT 1
		     FOR UPDATE SKIP LOCKED
		 )
		 RETURNING id, file_key, media_version, user_id, share_token,
		     COALESCE(transcription_language, (SELECT transcription_language FROM users WHERE id = videos.user_id), 'auto')`,
	).Scan(&videoID, &fileKey, &version, &userID, &shareToken, &language)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Error("transcribe-worker: failed to claim job", "error", err)
		}
		return
	}

	slog.Info("transcribe-worker: claimed video", "video_id", videoID)
	processTranscription(ctx, db, storage, transcriber, videoID, fileKey, version, userID, shareToken, language, aiEnabled)
}

func StartTranscriptionWorker(ctx context.Context, db database.DBTX, storage ObjectStorage, transcriber Transcriber, interval time.Duration, aiEnabled bool) {
	go func() {
		slog.Info("transcribe-worker: started", "provider", transcriber.Name())
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				slog.Info("transcribe-worker: shutting down")
				return
			case <-ticker.C:
				processNextTranscription(ctx, db, storage, transcriber, aiEnabled)
			}
		}
	}()
}
