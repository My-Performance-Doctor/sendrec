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
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/transcribe"
	"github.com/aws/aws-sdk-go-v2/service/transcribe/types"
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
type transcribeStorage interface {
	UploadFile(ctx context.Context, key, filePath, contentType string) error
	DownloadToFile(ctx context.Context, key, destPath string) error
	DeleteObject(ctx context.Context, key string) error
}

// awsTranscriber runs Amazon Transcribe batch jobs on audio staged in the
// recordings bucket. The audio never leaves MPD's account and region.
type awsTranscriber struct {
	api      transcribeAPI
	storage  transcribeStorage
	bucket   string
	language string        // used for "auto" and "en", e.g. en-AU
	poll     time.Duration // how often a running job is checked
}

func newAWSTranscriber(api transcribeAPI, storage transcribeStorage, bucket, language string) *awsTranscriber {
	if language == "" {
		language = "en-AU"
	}
	return &awsTranscriber{api: api, storage: storage, bucket: bucket, language: language, poll: 3 * time.Second}
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

	// Cleanup runs on a context of its own: the job's may have run out, and the
	// audio is the patient's.
	started := false
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		for _, key := range []string{audioKey, outputKey} {
			if err := a.storage.DeleteObject(cleanup, key); err != nil {
				slog.Warn("transcribe: failed to delete temporary object", "key", key, "error", err)
			}
		}
		if started {
			if _, err := a.api.DeleteTranscriptionJob(cleanup, &transcribe.DeleteTranscriptionJobInput{TranscriptionJobName: aws.String(job)}); err != nil {
				slog.Warn("transcribe: failed to delete job", "job", job, "error", err)
			}
		}
	}()

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
	if code, identify := a.languageFor(language); identify {
		input.IdentifyLanguage = aws.Bool(true)
	} else {
		input.LanguageCode = types.LanguageCode(code)
	}
	if _, err := a.api.StartTranscriptionJob(ctx, input); err != nil {
		return nil, fmt.Errorf("start job: %w", err)
	}
	started = true

	if err := a.wait(ctx, job); err != nil {
		return nil, err
	}

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

// wait polls the job until it completes, fails or the context ends.
func (a *awsTranscriber) wait(ctx context.Context, job string) error {
	ticker := time.NewTicker(a.poll)
	defer ticker.Stop()
	for {
		got, err := a.api.GetTranscriptionJob(ctx, &transcribe.GetTranscriptionJobInput{TranscriptionJobName: aws.String(job)})
		if err != nil {
			return fmt.Errorf("get job: %w", err)
		}
		switch got.TranscriptionJob.TranscriptionJobStatus {
		case types.TranscriptionJobStatusCompleted:
			return nil
		case types.TranscriptionJobStatusFailed:
			return fmt.Errorf("job failed: %s", aws.ToString(got.TranscriptionJob.FailureReason))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// languageFor maps SendRec's language setting to Transcribe's. "auto" and
// "en" use the configured locale, since MPD's Doctors speak Australian
// English; a full locale is used as given; anything else is identified.
func (a *awsTranscriber) languageFor(language string) (code string, identify bool) {
	switch {
	case language == "" || language == "auto" || language == "en":
		return a.language, false
	case strings.Contains(language, "-"):
		return language, false
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
