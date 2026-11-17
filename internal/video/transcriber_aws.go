package video

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/transcribe"
	"github.com/aws/aws-sdk-go-v2/service/transcribe/types"
	"github.com/aws/smithy-go"
)

// transcribePrefix holds the audio and output of running jobs. The stack
// expires everything under it, current and noncurrent, after a day, so a task
// that stops mid-job leaves nothing behind for long.
const transcribePrefix = "transcribe/"

// transcribeAPI is the part of the Amazon Transcribe client the provider uses.
type transcribeAPI interface {
	StartTranscriptionJob(context.Context, *transcribe.StartTranscriptionJobInput, ...func(*transcribe.Options)) (*transcribe.StartTranscriptionJobOutput, error)
	GetTranscriptionJob(context.Context, *transcribe.GetTranscriptionJobInput, ...func(*transcribe.Options)) (*transcribe.GetTranscriptionJobOutput, error)
	DeleteTranscriptionJob(context.Context, *transcribe.DeleteTranscriptionJobInput, ...func(*transcribe.Options)) (*transcribe.DeleteTranscriptionJobOutput, error)
}

// transcribeStorage is the part of the recordings bucket the provider uses.
// DeleteObjectVersions removes every version and delete marker of a key, so
// in the versioned production bucket no noncurrent copy of the patient's
// audio or transcript is left for the lifecycle rule.
type transcribeStorage interface {
	UploadFile(ctx context.Context, key, filePath, contentType string) error
	DownloadToFile(ctx context.Context, key, destPath string) error
	DeleteObjectVersions(ctx context.Context, key string) error
}

// awsTranscriber runs Amazon Transcribe batch jobs on audio staged in the
// recordings bucket. The audio never leaves MPD's account and region.
type awsTranscriber struct {
	api      transcribeAPI
	storage  transcribeStorage
	bucket   string
	language string        // used for "auto" and "en", e.g. en-AU
	poll     time.Duration // how often a running job is checked
	maxPoll  time.Duration // the longest wait between checks after errors
	// settle bounds how long cleanup waits for a job that was still running
	// when it was abandoned. Transcribe cannot cancel a job, so the job may
	// still write its output; cleanup deletes it once it lands.
	settle time.Duration
	// startAttempts is how often a job start is tried before giving up.
	startAttempts int
}

func newAWSTranscriber(api transcribeAPI, storage transcribeStorage, bucket, language string) *awsTranscriber {
	if language == "" {
		language = "en-AU"
	}
	return &awsTranscriber{api: api, storage: storage, bucket: bucket, language: language,
		poll: 3 * time.Second, maxPoll: 30 * time.Second, settle: 3 * time.Minute, startAttempts: 3}
}

func (a *awsTranscriber) Name() string { return "aws-transcribe" }

func (a *awsTranscriber) Available() bool {
	return a.api != nil && a.storage != nil && a.bucket != ""
}

func (a *awsTranscriber) Transcribe(ctx context.Context, audioPath, language string) ([]TranscriptSegment, error) {
	id := make([]byte, 12)
	if _, err := rand.Read(id); err != nil {
		return nil, fmt.Errorf("job name: %w", err)
	}
	job := "sendrec-" + hex.EncodeToString(id)
	audioKey := transcribePrefix + job + ".wav"
	outputKey := transcribePrefix + job + ".json"

	// exists says whether a job may exist to settle and delete: true from the
	// start call on, since a start with a lost response may have created it,
	// unless the start's own probe proved there is none. finished records
	// that the job reached a final state. late, for a job abandoned while
	// running, receives its transcript if it completes during the settle.
	var (
		exists, finished bool
		startedAt        time.Time
		late             chan []TranscriptSegment
	)
	defer func() { a.cleanup(ctx, job, audioKey, outputKey, exists, finished, startedAt, late) }()

	if err := a.storage.UploadFile(ctx, audioKey, audioPath, "audio/wav"); err != nil {
		return nil, fmt.Errorf("upload audio: %w", err)
	}
	input := &transcribe.StartTranscriptionJobInput{
		TranscriptionJobName: aws.String(job),
		Media:                &types.Media{MediaFileUri: aws.String("s3://" + a.bucket + "/" + audioKey)},
		MediaFormat:          types.MediaFormatWav,
		OutputBucketName:     aws.String(a.bucket),
		OutputKey:            aws.String(outputKey),
	}
	// The worker logs SendRec's setting ("auto" for most recordings); log what
	// Transcribe is actually told, the configured locale or "identify".
	given := "identify"
	if code, identify := a.languageFor(language); identify {
		input.IdentifyLanguage = aws.Bool(true)
	} else {
		input.LanguageCode = types.LanguageCode(code)
		given = code
	}
	slog.Info("transcribe: job language", "job", job, "requested", language, "transcribe_language", given)
	exists = true
	// startedAt is the first start attempt: the read-after-create grace runs
	// from there, for the start's probes, the wait and the settle alike.
	startedAt = time.Now()
	ok, err := a.start(ctx, input, startedAt)
	exists = ok
	if err != nil {
		return nil, err
	}

	err = a.wait(ctx, job, startedAt)
	finished = err == nil || errors.Is(err, errJobFailed)
	if err != nil {
		if !finished {
			late = make(chan []TranscriptSegment, 1)
			return nil, &abandonedJobError{err: err, late: late}
		}
		return nil, err
	}
	return a.readOutput(ctx, outputKey)
}

// readOutput downloads and parses a completed job's output.
func (a *awsTranscriber) readOutput(ctx context.Context, outputKey string) ([]TranscriptSegment, error) {
	tmp, err := os.CreateTemp("", "sendrec-transcribe-*.json")
	if err != nil {
		return nil, fmt.Errorf("create temp output: %w", err)
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := a.storage.DownloadToFile(ctx, outputKey, tmpPath); err != nil {
		return nil, fmt.Errorf("download output: %w", err)
	}
	data, err := os.ReadFile(tmpPath)
	if err != nil {
		return nil, fmt.Errorf("read output: %w", err)
	}
	var out transcribeOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse output: %w", err)
	}
	segments := transcribeSegments(out.Results.Items)
	if len(segments) == 0 {
		return nil, ErrNoAudio
	}
	return segments, nil
}

// abandonedJobError is returned for a job left running at its deadline or
// after a status check failed for good. Late receives the job's transcript if
// it completes during the cleanup's settle, and is closed when the settle ends.
type abandonedJobError struct {
	err  error
	late <-chan []TranscriptSegment
}

func (e *abandonedJobError) Error() string { return e.err.Error() }
func (e *abandonedJobError) Unwrap() error { return e.err }

// errJobFailed marks a job Transcribe itself reported as failed.
var errJobFailed = errors.New("transcription job failed")

// readAfterCreateGrace is how long after a successful start a missing-job
// answer is taken as the service not having caught up yet, rather than as
// the job being gone. A var so tests can shorten it.
var readAfterCreateGrace = 60 * time.Second

// retryableTranscribeError reports whether an AWS error may clear on its own:
// throttling, limits, the service's own 5xx faults, and errors that never
// reached the service, such as a network failure. Access, validation and
// unsupported-language errors will not, so the job goes to the fallback at once.
func retryableTranscribeError(err error) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return true
	}
	switch apiErr.ErrorCode() {
	case "ThrottlingException", "TooManyRequestsException", "LimitExceededException",
		"InternalFailureException", "ServiceUnavailableException", "RequestTimeout", "RequestTimeoutException":
		return true
	}
	var httpErr *awshttp.ResponseError
	if errors.As(err, &httpErr) && httpErr.HTTPStatusCode() >= 500 {
		return true
	}
	return apiErr.ErrorFault() == smithy.FaultServer
}

// jobMissing reports whether Transcribe says the job does not exist. The real
// API answers a missing job with BadRequestException "couldn't be found".
func jobMissing(err error) bool {
	var missing *types.NotFoundException
	if errors.As(err, &missing) {
		return true
	}
	var bad *types.BadRequestException
	return errors.As(err, &bad) && strings.Contains(strings.ToLower(aws.ToString(bad.Message)), "couldn't be found")
}

// logAccessDenied names a denied call as the configuration error it is.
// Denied errors are not retried, so this logs once per job.
func logAccessDenied(job string, err error) {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "AccessDeniedException" || apiErr.ErrorCode() == "AccessDenied") {
		slog.Error("transcribe: access denied; check the task role's Transcribe and recordings-bucket permissions",
			"job", job, "error", err)
	}
}

// start starts the job, retrying errors that may clear. A start whose response
// was lost may have created the job anyway, so each failure first asks whether
// the job exists, and a ConflictException says an earlier attempt created
// it. It reports whether a job may exist: false when the start was refused
// outright (access, validation), which creates no job, or when a probe past
// the read-after-create grace finds none.
func (a *awsTranscriber) start(ctx context.Context, input *transcribe.StartTranscriptionJobInput, firstAttempt time.Time) (bool, error) {
	job := aws.ToString(input.TranscriptionJobName)
	exists := true
	var err error
	for attempt := 1; attempt <= a.startAttempts; attempt++ {
		if _, err = a.api.StartTranscriptionJob(ctx, input); err == nil {
			return true, nil
		}
		var conflict *types.ConflictException
		if errors.As(err, &conflict) {
			return true, nil // an earlier attempt's lost response created it
		}
		logAccessDenied(job, err)
		slog.Warn("transcribe: start failed", "job", job, "attempt", attempt, "error", err)
		if !retryableTranscribeError(err) {
			// Refused outright: no job was created, so nothing to settle.
			return false, fmt.Errorf("start job: %w", err)
		}
		_, probeErr := a.api.GetTranscriptionJob(ctx, &transcribe.GetTranscriptionJobInput{TranscriptionJobName: input.TranscriptionJobName})
		if probeErr == nil {
			return true, nil
		}
		// A missing job is proof only once the read-after-create grace is over.
		exists = !jobMissing(probeErr) || time.Since(firstAttempt) < readAfterCreateGrace
		if attempt < a.startAttempts {
			select {
			case <-ctx.Done():
				return exists, ctx.Err()
			case <-time.After(time.Duration(attempt) * a.poll):
			}
		}
	}
	return exists, fmt.Errorf("start job: %w", err)
}

// wait polls the job until it completes, fails or the context ends. A poll
// that fails with an error that may clear, such as throttling or a network
// error, or a missing-job answer just after the start, is retried with a
// longer wait until the job's deadline; any other error ends the wait so the
// fallback can start.
func (a *awsTranscriber) wait(ctx context.Context, job string, startedAt time.Time) error {
	delay := a.poll
	for {
		got, err := a.api.GetTranscriptionJob(ctx, &transcribe.GetTranscriptionJobInput{TranscriptionJobName: aws.String(job)})
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			catchingUp := jobMissing(err) && time.Since(startedAt) < readAfterCreateGrace
			if !catchingUp && !retryableTranscribeError(err) {
				logAccessDenied(job, err)
				return fmt.Errorf("get job: %w", err)
			}
			slog.Warn("transcribe: checking the job failed, retrying", "job", job, "error", err)
			delay = min(delay*2, a.maxPoll)
		case got.TranscriptionJob.TranscriptionJobStatus == types.TranscriptionJobStatusCompleted:
			return nil
		case got.TranscriptionJob.TranscriptionJobStatus == types.TranscriptionJobStatusFailed:
			return fmt.Errorf("%w: %s", errJobFailed, aws.ToString(got.TranscriptionJob.FailureReason))
		default:
			delay = a.poll
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
}

// stoppingSettle bounds a settle once the task has begun to stop. A var so
// tests can shorten it.
var stoppingSettle = 5 * time.Second

// transcribeCleanups tracks background cleanups of abandoned jobs, so
// shutdown can wait for them.
var transcribeCleanups sync.WaitGroup

// cleanup deletes the patient's audio at once. For a finished job, or none,
// it deletes the output and any job before returning. A job still running,
// abandoned at its deadline or a task's shutdown, may still write its output,
// because Transcribe cannot cancel a job: a background cleanup on a context
// of its own waits a bounded time for it, hands a completed transcript to
// late, then deletes the output and the job. The caller returns at once, so a
// fallback starts with its whole reserve. The transcribe/ lifecycle rule
// removes anything a cleanup misses.
func (a *awsTranscriber) cleanup(ctx context.Context, job, audioKey, outputKey string, exists, finished bool, startedAt time.Time, late chan []TranscriptSegment) {
	base := context.WithoutCancel(ctx)
	dctx, cancel := context.WithTimeout(base, 15*time.Second)
	a.deleteKey(dctx, audioKey)
	cancel()
	finish := func(fctx context.Context) {
		a.deleteKey(fctx, outputKey)
		if exists {
			if _, err := a.api.DeleteTranscriptionJob(fctx, &transcribe.DeleteTranscriptionJobInput{TranscriptionJobName: aws.String(job)}); err != nil && !jobMissing(err) {
				slog.Warn("transcribe: failed to delete job", "job", job, "error", err)
			}
		}
	}
	// late is only made for a job that exists and did not finish, so this
	// branch never has one to close.
	if !exists || finished {
		fctx, cancel := context.WithTimeout(base, 15*time.Second)
		defer cancel()
		finish(fctx)
		return
	}
	transcribeCleanups.Add(1)
	go func() {
		defer transcribeCleanups.Done()
		sctx, cancel := context.WithTimeout(base, a.settle+15*time.Second)
		defer cancel()
		status := a.settleJob(sctx, job, a.settle, startedAt)
		if late != nil {
			if status == types.TranscriptionJobStatusCompleted {
				if segments, err := a.readOutput(sctx, outputKey); err == nil {
					late <- segments
				}
			}
			close(late)
		}
		finish(sctx)
	}()
}

// settleJob waits up to settle for an abandoned job to reach a final state and
// returns that state, or "" when it did not or the job is gone. A missing-job
// answer within readAfterCreateGrace of the start is waited out. If the task
// begins to stop meanwhile, it waits at most stoppingSettle more.
func (a *awsTranscriber) settleJob(ctx context.Context, job string, settle time.Duration, startedAt time.Time) types.TranscriptionJobStatus {
	deadline := time.Now().Add(settle)
	var stopSeen time.Time
	for {
		limit := deadline
		if transcriptionStopping.Load() {
			if stopSeen.IsZero() {
				stopSeen = time.Now()
			}
			if stopLimit := stopSeen.Add(stoppingSettle); stopLimit.Before(limit) {
				limit = stopLimit
			}
		}
		if !time.Now().Before(limit) {
			break
		}
		got, err := a.api.GetTranscriptionJob(ctx, &transcribe.GetTranscriptionJobInput{TranscriptionJobName: aws.String(job)})
		if err != nil {
			if jobMissing(err) && (startedAt.IsZero() || time.Since(startedAt) >= readAfterCreateGrace) {
				return ""
			}
		} else if s := got.TranscriptionJob.TranscriptionJobStatus; s == types.TranscriptionJobStatusCompleted || s == types.TranscriptionJobStatusFailed {
			return s
		}
		select {
		case <-ctx.Done():
			return ""
		case <-time.After(min(a.poll, time.Until(limit)+time.Millisecond)):
		}
	}
	slog.Warn("transcribe: abandoned job still running at cleanup; the lifecycle rule removes its output", "job", job)
	return ""
}

func (a *awsTranscriber) deleteKey(ctx context.Context, key string) {
	if err := a.storage.DeleteObjectVersions(ctx, key); err != nil {
		slog.Warn("transcribe: failed to delete temporary object", "key", key, "error", err)
	}
}

// transcribeLocales maps SendRec's two-letter language choices to the
// Transcribe locale a user who picked them most likely means. A choice not
// listed here is identified from the audio.
var transcribeLocales = map[string]string{
	"ar": "ar-SA", "da": "da-DK", "de": "de-DE", "es": "es-ES", "fr": "fr-FR",
	"hi": "hi-IN", "it": "it-IT", "ja": "ja-JP", "ko": "ko-KR", "nl": "nl-NL",
	"pt": "pt-BR", "ru": "ru-RU", "sv": "sv-SE", "tr": "tr-TR", "zh": "zh-CN",
}

// languageFor maps SendRec's language setting to Transcribe's. "auto" and
// "en" use the configured locale, since MPD's Doctors speak Australian
// English; a full locale is used as given; a common language uses its usual
// locale, so an explicit choice is kept; anything else is identified.
func (a *awsTranscriber) languageFor(language string) (code string, identify bool) {
	switch {
	case language == "" || language == "auto" || language == "en":
		return a.language, false
	case strings.Contains(language, "-"):
		return language, false
	case transcribeLocales[language] != "":
		return transcribeLocales[language], false
	default:
		return "", true
	}
}

type transcribeOutput struct {
	Results struct {
		Items []transcribeItem `json:"items"`
	} `json:"results"`
}

type transcribeItem struct {
	Type         string `json:"type"`
	StartTime    string `json:"start_time"`
	EndTime      string `json:"end_time"`
	Alternatives []struct {
		Content string `json:"content"`
	} `json:"alternatives"`
}

// Caption segments close at a sentence end, after a pause or once they run
// this long, so each reads as one line with its own time.
const (
	transcribeMaxPause   = 1.5
	transcribeMaxSegment = 10.0
)

// transcribeSegments groups Transcribe's words into caption segments.
func transcribeSegments(items []transcribeItem) []TranscriptSegment {
	var segments []TranscriptSegment
	var current *TranscriptSegment
	flush := func() {
		if current != nil {
			segments = append(segments, *current)
			current = nil
		}
	}
	for _, item := range items {
		if len(item.Alternatives) == 0 {
			continue
		}
		content := item.Alternatives[0].Content
		if item.Type == "punctuation" {
			if current != nil {
				current.Text += content
				if strings.ContainsAny(content, ".?!") {
					flush()
				}
			}
			continue
		}
		start, err1 := strconv.ParseFloat(item.StartTime, 64)
		end, err2 := strconv.ParseFloat(item.EndTime, 64)
		if err := errors.Join(err1, err2); err != nil || content == "" {
			continue
		}
		if current != nil && (start-current.End > transcribeMaxPause || end-current.Start > transcribeMaxSegment) {
			flush()
		}
		if current == nil {
			current = &TranscriptSegment{Start: start, End: end, Text: content}
			continue
		}
		current.Text += " " + content
		current.End = end
	}
	flush()
	return segments
}
