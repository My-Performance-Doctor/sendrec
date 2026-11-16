package video

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sendrec/sendrec/internal/database"
)

// transcriptionJobTimeout is the base of every transcription deadline,
// download to upload, so a hung whisper-cli or ffmpeg cannot hold the queue.
// A var so tests can shorten it.
var transcriptionJobTimeout = 30 * time.Minute

// transcriptionSecondsPerSecond is how much deadline each second of recording
// adds. Whisper's small model on a 2 vCPU task ran at 1.0× a 10 minute
// recording, 2.4× on dense speech, and about twice as slow on a slow host.
// Encodes also take the CPU first (localWhisper runs at the lowest priority),
// so the job can sit waiting for them as well.
const transcriptionSecondsPerSecond = 4

// transcriptionExtraCapSeconds bounds how far a long recording stretches the
// deadline, so a bogus duration cannot hold the worker for days.
const transcriptionExtraCapSeconds = 4 * 60 * 60

// transcriptionTimeout is the base plus four times the recording's length. A
// fixed 30 minutes failed 10 minute recordings, and a failed transcript means
// MPD never creates the patient's report. A zero duration, as for a recording
// not yet probed, gives the base alone. The stuck-job reset in
// processNextTranscription applies the same formula in SQL.
func transcriptionTimeout(durationSeconds int) time.Duration {
	extra := min(max(transcriptionSecondsPerSecond*durationSeconds, 0), transcriptionExtraCapSeconds)
	return transcriptionJobTimeout + time.Duration(extra)*time.Second
}

func EnqueueTranscription(ctx context.Context, db database.DBTX, videoID string) error {
	if !isTranscriptionEnabled() {
		return nil
	}
	_, err := db.Exec(ctx,
		`UPDATE videos SET transcript_status = 'pending', transcript_generation = transcript_generation + 1, updated_at = now()
		 WHERE id = $1 AND status != 'deleted'`,
		videoID,
	)
	return err
}

// transcriptionJob is a claimed job, as the claim returned it.
type transcriptionJob struct {
	videoID    string
	generation int
}

// inFlightTranscription is the job this process is running, if any.
// transcriptionStopping stops the worker claiming once shutdown has begun.
var (
	inFlightTranscription atomic.Pointer[transcriptionJob]
	transcriptionStopping atomic.Bool
)

// RequeueInFlightTranscription hands this process's running job back to the
// queue at shutdown, as during a deploy or a scale-in. Without it the job
// waits out its whole deadline, which grows with the recording, before
// another task may start it. The generation bump discards any result this
// process still writes. The worker claims nothing after this.
func RequeueInFlightTranscription(ctx context.Context, db database.DBTX) {
	transcriptionStopping.Store(true)
	job := inFlightTranscription.Load()
	if job == nil {
		return
	}
	if _, err := db.Exec(ctx,
		`UPDATE videos SET transcript_status = 'pending', transcript_generation = transcript_generation + 1, transcript_started_at = NULL, updated_at = now()
		 WHERE id = $1 AND transcript_status = 'processing' AND transcript_generation = $2`,
		job.videoID, job.generation,
	); err != nil {
		slog.Error("transcribe-worker: failed to requeue the running job at shutdown", "video_id", job.videoID, "error", err)
		return
	}
	slog.Info("transcribe-worker: requeued the running job at shutdown", "video_id", job.videoID)
}

func processNextTranscription(ctx context.Context, db database.DBTX, storage ObjectStorage, transcriber Transcriber, aiEnabled bool) {
	if transcriptionStopping.Load() {
		return
	}
	// Requeue jobs whose worker died. A live job gives up at its deadline,
	// which grows with the recording, so anything a minute past it belongs to
	// nobody.
	if _, err := db.Exec(ctx,
		`UPDATE videos SET transcript_status = 'pending', transcript_generation = transcript_generation + 1, transcript_started_at = NULL, updated_at = now()
		 WHERE transcript_status = 'processing'
		   AND (transcript_started_at < now() - make_interval(secs => $1::float8 + LEAST(GREATEST($2::bigint * duration, 0), $3::bigint))
		        OR transcript_started_at IS NULL)`,
		(transcriptionJobTimeout + time.Minute).Seconds(), transcriptionSecondsPerSecond, transcriptionExtraCapSeconds,
	); err != nil {
		slog.Error("transcribe-worker: failed to reset stuck jobs", "error", err)
	}

	// Whisper yields the CPU to encodes, so a job started beside one waits for
	// it with its deadline running. While this task encodes, leave the job to
	// an idle task, or to this one on a later tick.
	if encoders().busy() {
		return
	}

	// Claim the next pending job
	var videoID, fileKey, userID, shareToken, language string
	var version, duration, generation int
	err := db.QueryRow(ctx,
		`UPDATE videos SET transcript_status = 'processing', transcript_started_at = now(), updated_at = now()
		 WHERE id = (
		     SELECT v.id FROM videos v
		     WHERE v.transcript_status = 'pending' AND v.status != 'deleted'
		     ORDER BY v.updated_at ASC LIMIT 1
		     FOR UPDATE SKIP LOCKED
		 )
		 RETURNING id, file_key, media_version, duration, user_id, share_token,
		     COALESCE(transcription_language, (SELECT transcription_language FROM users WHERE id = videos.user_id), 'auto'), transcript_generation`,
	).Scan(&videoID, &fileKey, &version, &duration, &userID, &shareToken, &language, &generation)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Error("transcribe-worker: failed to claim job", "error", err)
		}
		return
	}

	slog.Info("transcribe-worker: claimed video", "video_id", videoID)
	inFlightTranscription.Store(&transcriptionJob{videoID: videoID, generation: generation})
	defer inFlightTranscription.Store(nil)
	// Shutdown can begin during the claim, after RequeueInFlightTranscription
	// found nothing running. Hand the job back now rather than leave it to the
	// stuck-job reset, which waits out the whole deadline.
	if transcriptionStopping.Load() {
		requeueCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		RequeueInFlightTranscription(requeueCtx, db)
		return
	}
	processTranscription(ctx, db, storage, transcriber, videoID, fileKey, version, duration, userID, shareToken, language, aiEnabled, generation)
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
