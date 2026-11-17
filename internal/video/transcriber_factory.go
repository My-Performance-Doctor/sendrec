package video

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/transcribe"
)

// NewTranscriberFromEnv builds a Transcriber from the TRANSCRIPTION_PROVIDER
// environment variable. Supported providers:
//   - "" or "local": runs whisper-cli locally (default).
//   - "openai":      OpenAI-compatible /v1/audio/transcriptions endpoint.
//     Works with OpenAI, Groq, Scaleway, self-hosted Faster-Whisper, etc.
//     Reads TRANSCRIPTION_API_URL, TRANSCRIPTION_API_KEY, TRANSCRIPTION_MODEL.
//   - "deepgram":    Deepgram /v1/listen.
//     Reads TRANSCRIPTION_API_KEY, TRANSCRIPTION_MODEL.
//   - "aws":         Amazon Transcribe batch jobs, staged in the recordings
//     bucket (S3_BUCKET) in S3_REGION, with the task's own credentials.
//     Reads TRANSCRIPTION_LANGUAGE_CODE (default en-AU). Falls back to local
//     Whisper for a job Transcribe cannot finish.
func NewTranscriberFromEnv(storage ObjectStorage) (Transcriber, error) {
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("TRANSCRIPTION_PROVIDER")))
	switch provider {
	case "", "local":
		return newLocalWhisper(), nil
	case "openai":
		key := os.Getenv("TRANSCRIPTION_API_KEY")
		if key == "" {
			return nil, fmt.Errorf("TRANSCRIPTION_PROVIDER=openai requires TRANSCRIPTION_API_KEY")
		}
		return newOpenAIWhisper(
			os.Getenv("TRANSCRIPTION_API_URL"),
			key,
			os.Getenv("TRANSCRIPTION_MODEL"),
			parseTimeoutEnv("TRANSCRIPTION_TIMEOUT_SECONDS"),
		), nil
	case "deepgram":
		key := os.Getenv("TRANSCRIPTION_API_KEY")
		if key == "" {
			return nil, fmt.Errorf("TRANSCRIPTION_PROVIDER=deepgram requires TRANSCRIPTION_API_KEY")
		}
		return newDeepgram(
			key,
			os.Getenv("TRANSCRIPTION_MODEL"),
			parseTimeoutEnv("TRANSCRIPTION_TIMEOUT_SECONDS"),
		), nil
	case "aws":
		bucket := os.Getenv("S3_BUCKET")
		if bucket == "" {
			return nil, fmt.Errorf("TRANSCRIPTION_PROVIDER=aws requires S3_BUCKET")
		}
		if storage == nil {
			return nil, fmt.Errorf("TRANSCRIPTION_PROVIDER=aws requires the recordings bucket")
		}
		cfg, err := config.LoadDefaultConfig(context.Background(), config.WithRegion(os.Getenv("S3_REGION")))
		if err != nil {
			return nil, fmt.Errorf("TRANSCRIPTION_PROVIDER=aws: load AWS config: %w", err)
		}
		staged, ok := storage.(transcribeStorage)
		if !ok {
			return nil, fmt.Errorf("TRANSCRIPTION_PROVIDER=aws requires storage that can delete object versions")
		}
		// Local Whisper stays in the image as the fallback when Transcribe fails.
		// The entrypoint downloads its model for this provider too.
		whisper := newLocalWhisper()
		if !whisper.Available() {
			slog.Warn("transcriber: local Whisper fallback unavailable; a failed Transcribe job will fail its transcript",
				"model_path", whisperModelPath())
		}
		return &fallbackTranscriber{
			primary:  newAWSTranscriber(transcribe.NewFromConfig(cfg), staged, bucket, os.Getenv("TRANSCRIPTION_LANGUAGE_CODE")),
			fallback: whisper,
		}, nil
	default:
		return nil, fmt.Errorf("unknown TRANSCRIPTION_PROVIDER %q (expected one of: local, openai, deepgram, aws)", provider)
	}
}

func parseTimeoutEnv(name string) time.Duration {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return 0
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}
