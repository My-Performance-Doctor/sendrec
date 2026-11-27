package video

import (
	"context"
	"errors"
	"log/slog"
	"sync"
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
// claimNextTranscription applies the same formula in SQL.
func transcriptionTimeout(durationSeconds int) time.Duration {
	extra := min(max(transcriptionSecondsPerSecond*durationSeconds, 0), transcriptionExtraCapSeconds)
	return transcriptionJobTimeout + time.Duration(extra)*time.Second
}

// DefaultTranscriptionConcurrency is how many transcriptions one process runs
// at once when MAX_CONCURRENT_TRANSCRIPTIONS is unset. A remote provider spends
// most of a job waiting on the network, so a few at once cost the task little.
// Local Whisper is CPU bound and always runs one at a time.
const DefaultTranscriptionConcurrency = 3

// transcriptionWake starts the worker at once instead of on its next tick. It
// holds at most one signal: one pass claims every job that fits.
var transcriptionWake = make(chan struct{}, 1)

func wakeTranscriptionWorker() {
	select {
	case transcriptionWake <- struct{}{}:
	default:
	}
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
	if err == nil {
		// This task's worker claims the job now. The poll still finds it, on
		// another task or after a failed claim.
		wakeTranscriptionWorker()
	}
	return err
}

// transcriptionJob is a claimed job, as the claim returned it.
type transcriptionJob struct {
	videoID, fileKey, userID, shareToken, language string
	version, duration, generation                  int
}

// inFlightJobs is the set of jobs this process is running.
type inFlightJobs struct {
	mu   sync.Mutex
	jobs map[*transcriptionJob]struct{}
}

func (s *inFlightJobs) add(job *transcriptionJob) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jobs == nil {
		s.jobs = map[*transcriptionJob]struct{}{}
	}
	s.jobs[job] = struct{}{}
}

func (s *inFlightJobs) remove(job *transcriptionJob) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, job)
}

func (s *inFlightJobs) list() []*transcriptionJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs := make([]*transcriptionJob, 0, len(s.jobs))
	for job := range s.jobs {
		jobs = append(jobs, job)
	}
	return jobs
}

func (s *inFlightJobs) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.jobs)
}

// inFlightTranscriptions are the jobs this process is running.
// transcriptionStopping stops the worker claiming once shutdown has begun.
var (
	inFlightTranscriptions inFlightJobs
	transcriptionStopping  atomic.Bool
)

// transcriptionCancel stops the transcription worker's context at shutdown.
var transcriptionCancel atomic.Pointer[context.CancelFunc]

// TranscriptionContext gives the transcription worker a context of its own,
// which StopTranscription cancels at shutdown. The rest of the process keeps
// running until main returns, so without it a provider's cleanup, such as
// deleting a patient's staged audio, would never run before the task goes.
func TranscriptionContext(parent context.Context) context.Context {
	ctx, cancel := context.WithCancel(parent)
	transcriptionCancel.Store(&cancel)
	return ctx
}

// StopTranscription cancels the worker's context and waits up to wait for the
// running jobs, if any, to return and finish their cleanup. Call it after
// RequeueInFlightTranscription, so the jobs are already back in the queue and
// any result the stopped jobs still write is discarded by their generation.
func StopTranscription(wait time.Duration) {
	if cancel := transcriptionCancel.Load(); cancel != nil {
		(*cancel)()
	}
	deadline := time.Now().Add(wait)
	for inFlightTranscriptions.count() > 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if inFlightTranscriptions.count() > 0 {
		slog.Warn("transcribe-worker: running jobs did not finish their cleanup before shutdown")
	}
	// Background cleanups of abandoned remote jobs shorten their wait once the
	// task is stopping; give them what is left.
	done := make(chan struct{})
	go func() { transcribeCleanups.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Until(deadline)):
		slog.Warn("transcribe-worker: remote job cleanup still running at shutdown; the lifecycle rule removes what is left")
	}
}

// RequeueInFlightTranscription hands this process's running jobs back to the
// queue at shutdown, as during a deploy or a scale-in. Without it a job
// waits out its whole deadline, which grows with the recording, before
// another task may start it. The generation bump discards any result this
// process still writes. The worker claims nothing after this.
func RequeueInFlightTranscription(ctx context.Context, db database.DBTX) {
	transcriptionStopping.Store(true)
	for _, job := range inFlightTranscriptions.list() {
		requeueTranscription(ctx, db, job)
	}
}

func requeueTranscription(ctx context.Context, db database.DBTX, job *transcriptionJob) {
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

// claimNextTranscription requeues jobs whose worker died, then claims the
// oldest pending job for this process and records it as running. It returns
// nil when there is no job, or when this task should leave it to another.
func claimNextTranscription(ctx context.Context, db database.DBTX, transcriber Transcriber) *transcriptionJob {
	if transcriptionStopping.Load() {
		return nil
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

	// Local Whisper yields the CPU to encodes, so a job started beside one
	// waits for it with its deadline running. While this task encodes, leave
	// the job to an idle task, or to this one on a later tick. A provider that
	// runs off the task has nothing to wait for.
	if _, local := transcriber.(*localWhisper); local && encoders().busy() {
		return nil
	}
	// Whisper runs one job at a time. While a remote provider's job has fallen
	// back to it, a further job here would wait for Whisper with its deadline
	// running, so leave it pending.
	if inFlightTranscriptions.count() > 0 && whisperInUse() {
		return nil
	}

	job := &transcriptionJob{}
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
	).Scan(&job.videoID, &job.fileKey, &job.version, &job.duration, &job.userID, &job.shareToken, &job.language, &job.generation)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Error("transcribe-worker: failed to claim job", "error", err)
		}
		return nil
	}

	slog.Info("transcribe-worker: claimed video", "video_id", job.videoID)
	inFlightTranscriptions.add(job)
	// Shutdown can begin during the claim, after RequeueInFlightTranscription
	// looked at the running jobs. Hand the job back now rather than leave it
	// to the stuck-job reset, which waits out the whole deadline.
	if transcriptionStopping.Load() {
		requeueCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		requeueTranscription(requeueCtx, db, job)
		inFlightTranscriptions.remove(job)
		return nil
	}
	return job
}

// runTranscription runs a claimed job and then forgets it.
func runTranscription(ctx context.Context, db database.DBTX, storage ObjectStorage, transcriber Transcriber, job *transcriptionJob, aiEnabled bool) {
	defer inFlightTranscriptions.remove(job)
	processTranscription(ctx, db, storage, transcriber, job.videoID, job.fileKey, job.version, job.duration, job.userID, job.shareToken, job.language, aiEnabled, job.generation)
}

// claimTranscriptions claims pending jobs while a slot is free and runs each
// in its own goroutine. A finishing job frees its slot and wakes the worker,
// so a queued job starts without waiting for the tick.
func claimTranscriptions(ctx context.Context, db database.DBTX, storage ObjectStorage, transcriber Transcriber, aiEnabled bool, slots chan struct{}) {
	for {
		select {
		case slots <- struct{}{}:
		default:
			return
		}
		job := claimNextTranscription(ctx, db, transcriber)
		if job == nil {
			<-slots
			return
		}
		go func() {
			defer func() {
				<-slots
				wakeTranscriptionWorker()
			}()
			runTranscription(ctx, db, storage, transcriber, job, aiEnabled)
		}()
	}
}

// StartTranscriptionWorker runs up to concurrency jobs at once. It claims
// when a job is enqueued in this process, when a job finishes, and every
// interval, which finds jobs enqueued on other tasks or left by a dead one.
// Local Whisper is CPU bound, so it always runs one job at a time.
func StartTranscriptionWorker(ctx context.Context, db database.DBTX, storage ObjectStorage, transcriber Transcriber, interval time.Duration, concurrency int, aiEnabled bool) {
	concurrency = transcriptionConcurrency(transcriber, concurrency)
	slots := make(chan struct{}, concurrency)
	go func() {
		slog.Info("transcribe-worker: started", "provider", transcriber.Name(), "concurrency", concurrency)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				slog.Info("transcribe-worker: shutting down")
				return
			case <-ticker.C:
			case <-transcriptionWake:
			}
			claimTranscriptions(ctx, db, storage, transcriber, aiEnabled, slots)
		}
	}()
}

func transcriptionConcurrency(transcriber Transcriber, limit int) int {
	if _, local := transcriber.(*localWhisper); local || limit < 1 {
		return 1
	}
	return limit
}
