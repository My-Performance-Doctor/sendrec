package video

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/transcribe"
	"github.com/aws/aws-sdk-go-v2/service/transcribe/types"
)

// fakeTranscribe runs a job by writing output into storage once it has been
// polled `polls` times.
type fakeTranscribe struct {
	mu       sync.Mutex
	storage  *memStorage
	output   string
	fail     string
	polls    int
	started  *transcribe.StartTranscriptionJobInput
	audio    string // the staged audio as Transcribe would have read it
	deleted  []string
	getCalls int
	// startErrs fails that many starts; createdAnyway makes a failed start
	// still create the job, as a lost response would.
	startErrs     int
	startCalls    int
	createdAnyway bool
	// getErrs fails that many polls of a started job, as throttling would.
	getErrs int
	// readyAt, when set, keeps the job running until then, whoever polls.
	readyAt time.Time
}

// DeleteObjectVersions on the in-memory bucket, which keeps no versions.
func (s *memStorage) DeleteObjectVersions(ctx context.Context, key string) error {
	return s.DeleteObject(ctx, key)
}

var errSyntheticAWS = errors.New("synthetic ThrottlingException")

func (f *fakeTranscribe) StartTranscriptionJob(_ context.Context, in *transcribe.StartTranscriptionJobInput, _ ...func(*transcribe.Options)) (*transcribe.StartTranscriptionJobOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls++
	if f.startCalls <= f.startErrs {
		if f.createdAnyway {
			f.started = in
		}
		return nil, errSyntheticAWS
	}
	f.started = in
	key := strings.TrimPrefix(aws.ToString(in.Media.MediaFileUri), "s3://recordings/")
	f.storage.mu.Lock()
	f.audio = f.storage.objects[key]
	f.storage.mu.Unlock()
	return &transcribe.StartTranscriptionJobOutput{}, nil
}

func (f *fakeTranscribe) GetTranscriptionJob(_ context.Context, in *transcribe.GetTranscriptionJobInput, _ ...func(*transcribe.Options)) (*transcribe.GetTranscriptionJobOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.started == nil {
		return nil, &types.NotFoundException{Message: aws.String("no such job")}
	}
	if f.getErrs > 0 {
		f.getErrs--
		return nil, errSyntheticAWS
	}
	f.getCalls++
	job := &types.TranscriptionJob{TranscriptionJobName: in.TranscriptionJobName, TranscriptionJobStatus: types.TranscriptionJobStatusInProgress}
	ready := f.getCalls > f.polls
	if !f.readyAt.IsZero() {
		ready = time.Now().After(f.readyAt)
	}
	if ready {
		if f.fail != "" {
			job.TranscriptionJobStatus = types.TranscriptionJobStatusFailed
			job.FailureReason = aws.String(f.fail)
		} else {
			job.TranscriptionJobStatus = types.TranscriptionJobStatusCompleted
			f.storage.mu.Lock()
			f.storage.objects[aws.ToString(f.started.OutputKey)] = f.output
			f.storage.mu.Unlock()
		}
	}
	return &transcribe.GetTranscriptionJobOutput{TranscriptionJob: job}, nil
}

func (f *fakeTranscribe) DeleteTranscriptionJob(_ context.Context, in *transcribe.DeleteTranscriptionJobInput, _ ...func(*transcribe.Options)) (*transcribe.DeleteTranscriptionJobOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, aws.ToString(in.TranscriptionJobName))
	return &transcribe.DeleteTranscriptionJobOutput{}, nil
}

const transcribeFixture = `{"results":{"items":[
 {"type":"pronunciation","start_time":"0.5","end_time":"0.9","alternatives":[{"content":"Hi"}]},
 {"type":"punctuation","alternatives":[{"content":","}]},
 {"type":"pronunciation","start_time":"1.0","end_time":"1.3","alternatives":[{"content":"Sam"}]},
 {"type":"punctuation","alternatives":[{"content":"."}]},
 {"type":"pronunciation","start_time":"1.6","end_time":"2.0","alternatives":[{"content":"Your"}]},
 {"type":"pronunciation","start_time":"2.0","end_time":"2.4","alternatives":[{"content":"ApoB"}]},
 {"type":"pronunciation","start_time":"2.4","end_time":"2.6","alternatives":[{"content":"is"}]},
 {"type":"pronunciation","start_time":"2.6","end_time":"3.1","alternatives":[{"content":"fine"}]},
 {"type":"punctuation","alternatives":[{"content":"."}]}
]}}`

func newFakeAWSTranscriber(t *testing.T, output string) (*awsTranscriber, *fakeTranscribe, *memStorage, string) {
	t.Helper()
	storage := newMemStorage(map[string]string{})
	api := &fakeTranscribe{storage: storage, output: output, polls: 2}
	tr := newAWSTranscriber(api, storage, "recordings", "")
	tr.poll, tr.maxPoll, tr.settle = time.Millisecond, 4*time.Millisecond, time.Second
	audio := filepath.Join(t.TempDir(), "audio.wav")
	if err := os.WriteFile(audio, []byte("synthetic wav"), 0o600); err != nil {
		t.Fatal(err)
	}
	return tr, api, storage, audio
}

func assertNothingStaged(t *testing.T, storage *memStorage, api *fakeTranscribe) {
	t.Helper()
	storage.mu.Lock()
	defer storage.mu.Unlock()
	for key := range storage.objects {
		if strings.HasPrefix(key, transcribePrefix) {
			t.Errorf("left %s in the bucket", key)
		}
	}
	if api.started != nil && (len(api.deleted) != 1 || api.deleted[0] != aws.ToString(api.started.TranscriptionJobName)) {
		t.Errorf("deleted jobs %v, want the one started", api.deleted)
	}
}

func TestAWSTranscriberTranscribesAndCleansUp(t *testing.T) {
	tr, api, storage, audio := newFakeAWSTranscriber(t, transcribeFixture)

	segments, err := tr.Transcribe(context.Background(), audio, "auto")
	if err != nil {
		t.Fatal(err)
	}

	want := []TranscriptSegment{{Start: 0.5, End: 1.3, Text: "Hi, Sam."}, {Start: 1.6, End: 3.1, Text: "Your ApoB is fine."}}
	if len(segments) != len(want) {
		t.Fatalf("segments = %+v, want %+v", segments, want)
	}
	for i := range want {
		if segments[i] != want[i] {
			t.Errorf("segment %d = %+v, want %+v", i, segments[i], want[i])
		}
	}
	in := api.started
	job := aws.ToString(in.TranscriptionJobName)
	if !strings.HasPrefix(job, "sendrec-") {
		t.Errorf("job name %q needs the sendrec- prefix the task role is scoped to", job)
	}
	if got := aws.ToString(in.Media.MediaFileUri); got != "s3://recordings/transcribe/"+job+".wav" {
		t.Errorf("media = %s", got)
	}
	if aws.ToString(in.OutputBucketName) != "recordings" || aws.ToString(in.OutputKey) != "transcribe/"+job+".json" {
		t.Errorf("output = %s/%s", aws.ToString(in.OutputBucketName), aws.ToString(in.OutputKey))
	}
	if in.LanguageCode != types.LanguageCodeEnAu || in.IdentifyLanguage != nil || in.MediaFormat != types.MediaFormatWav {
		t.Errorf("language %q identify %v format %q", in.LanguageCode, in.IdentifyLanguage, in.MediaFormat)
	}
	if api.audio != "synthetic wav" {
		t.Errorf("Transcribe would have read %q", api.audio)
	}
	assertNothingStaged(t, storage, api)
}

func TestAWSTranscriberFailedJobCleansUp(t *testing.T) {
	tr, api, storage, audio := newFakeAWSTranscriber(t, "")
	api.fail = "synthetic failure"

	_, err := tr.Transcribe(context.Background(), audio, "auto")
	if err == nil || !strings.Contains(err.Error(), "synthetic failure") {
		t.Fatalf("err = %v, want the failure reason", err)
	}
	assertNothingStaged(t, storage, api)
}

func TestAWSTranscriberSilence(t *testing.T) {
	tr, api, storage, audio := newFakeAWSTranscriber(t, `{"results":{"items":[]}}`)

	_, err := tr.Transcribe(context.Background(), audio, "auto")
	if !errors.Is(err, ErrNoAudio) {
		t.Fatalf("err = %v, want ErrNoAudio", err)
	}
	assertNothingStaged(t, storage, api)
}

// A task stopping mid-job still deletes the patient's audio and the job.
func TestAWSTranscriberCancelledCleansUp(t *testing.T) {
	tr, api, storage, audio := newFakeAWSTranscriber(t, transcribeFixture)
	api.polls = 1 << 30
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := tr.Transcribe(ctx, audio, "auto")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline", err)
	}
	assertNothingStaged(t, storage, api)
}

func TestAWSTranscriberLanguage(t *testing.T) {
	tr := newAWSTranscriber(nil, nil, "recordings", "")
	for _, tc := range []struct {
		in, code string
		identify bool
	}{
		{"auto", "en-AU", false},
		{"", "en-AU", false},
		{"en", "en-AU", false},
		{"en-GB", "en-GB", false},
		{"de", "de-DE", false},
		{"bo", "", true},
	} {
		code, identify := tr.languageFor(tc.in)
		if code != tc.code || identify != tc.identify {
			t.Errorf("%q: got %q %v, want %q %v", tc.in, code, identify, tc.code, tc.identify)
		}
	}
	if code, _ := newAWSTranscriber(nil, nil, "recordings", "en-GB").languageFor("auto"); code != "en-GB" {
		t.Errorf("configured language ignored: %q", code)
	}
}

func TestTranscribeSegmentsBreakOnPausesAndLength(t *testing.T) {
	word := func(start, end float64, text string) transcribeItem {
		item := transcribeItem{Type: "pronunciation", StartTime: strconvF(start), EndTime: strconvF(end)}
		item.Alternatives = append(item.Alternatives, struct {
			Content string `json:"content"`
		}{text})
		return item
	}
	// A 2 s pause splits; a 1 s one does not. Running past 10 s without a
	// full stop splits too.
	items := []transcribeItem{word(0, 1, "one"), word(3, 4, "two")}
	for i := range 12 {
		items = append(items, word(5+float64(i), 5.9+float64(i), "on"))
	}
	segments := transcribeSegments(items)
	if len(segments) != 3 || segments[0].Text != "one" || segments[1].Start != 3 || !strings.HasPrefix(segments[1].Text, "two on") {
		t.Fatalf("segments = %+v", segments)
	}
	for _, s := range segments {
		if s.End-s.Start > transcribeMaxSegment {
			t.Errorf("segment %+v runs past %v s", s, transcribeMaxSegment)
		}
	}
}

func strconvF(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func TestNewTranscriberFromEnv_AWSRequiresBucket(t *testing.T) {
	t.Setenv("TRANSCRIPTION_PROVIDER", "aws")
	t.Setenv("S3_BUCKET", "")
	if _, err := NewTranscriberFromEnv(newMemStorage(map[string]string{})); err == nil || !strings.Contains(err.Error(), "S3_BUCKET") {
		t.Fatalf("err = %v, want S3_BUCKET required", err)
	}
}

func TestNewTranscriberFromEnv_AWS(t *testing.T) {
	t.Setenv("TRANSCRIPTION_PROVIDER", "aws")
	t.Setenv("S3_BUCKET", "recordings")
	t.Setenv("S3_REGION", "ap-southeast-2")
	t.Setenv("TRANSCRIPTION_LANGUAGE_CODE", "")
	tr, err := NewTranscriberFromEnv(newMemStorage(map[string]string{}))
	if err != nil {
		t.Fatal(err)
	}
	if tr.Name() != "aws-transcribe" || !tr.Available() {
		t.Errorf("name %q available %v", tr.Name(), tr.Available())
	}
}
