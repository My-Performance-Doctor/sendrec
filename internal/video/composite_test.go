package video

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v5"
)

func TestWebcamFileKey(t *testing.T) {
	key := webcamFileKey("user-123", "abc123defghi", "video/webm")
	expected := "recordings/user-123/abc123defghi_webcam.webm"
	if key != expected {
		t.Errorf("expected %q, got %q", expected, key)
	}
}

func TestWebcamFileKey_MP4(t *testing.T) {
	key := webcamFileKey("user-123", "abc123defghi", "video/mp4")
	expected := "recordings/user-123/abc123defghi_webcam.mp4"
	if key != expected {
		t.Errorf("expected %q, got %q", expected, key)
	}
}

func TestCompositeWithWebcam_ScreenDownloadError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	s := &mockStorage{downloadToFileErr: fmt.Errorf("s3 down")}

	// On error, should still set status to "ready" (fallback to screen-only)
	mock.ExpectExec(`UPDATE videos SET status = 'ready', webcam_key = NULL, processing_started_at = NULL`).
		WithArgs("video-123", webcamDroppedWarning).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	CompositeWithWebcam(context.Background(), mock, s, "video-123",
		"recordings/user/video.webm", "recordings/user/video_webcam.webm", "recordings/user/video.jpg", "video/webm")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestCompositeWithWebcam_FFmpegFailsFallsBackToReady(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	// Download succeeds but ffmpeg will fail (no actual video content)
	s := &mockStorage{}

	// On error, should still set status to "ready"
	mock.ExpectExec(`UPDATE videos SET status = 'ready', webcam_key = NULL, processing_started_at = NULL`).
		WithArgs("video-123", webcamDroppedWarning).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	CompositeWithWebcam(context.Background(), mock, s, "video-123",
		"recordings/user/video.webm", "recordings/user/video_webcam.webm", "recordings/user/video.jpg", "video/webm")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// A WebM screen recording with a webcam used to be composited to VP9, which
// libvpx encodes on about one core, and then transcoded to MP4 by the worker
// anyway. It now goes straight to H.264, once. #279.
func TestBuildCompositeArgs_EncodesH264ForEveryInput(t *testing.T) {
	for _, in := range []string{"s.webm", "s.mp4", "s.mov"} {
		args := buildCompositeArgs(in, "w.webm", "out.mp4")
		if !slices.Contains(args, "libx264") || slices.Contains(args, "libvpx-vp9") {
			t.Errorf("%s: want libx264 and no libvpx-vp9, got %v", in, args)
		}
		if i := slices.Index(args, "-c:a"); i < 0 || args[i+1] != "aac" {
			t.Errorf("%s: want AAC audio, got %v", in, args)
		}
	}
}

func stubCompositeTools(t *testing.T) {
	t.Helper()
	origProbe, origOverlay, origStreams := probeVideoInfo, compositeOverlay, probeStreamDurations
	t.Cleanup(func() { probeVideoInfo, compositeOverlay, probeStreamDurations = origProbe, origOverlay, origStreams })
	probeVideoInfo = func(context.Context, string) (int, string, error) { return 30, "", nil }
	compositeOverlay = func(_ context.Context, _, _, out string) (string, error) {
		return "", os.WriteFile(out, []byte("mp4 bytes"), 0o600)
	}
	probeStreamDurations = func(context.Context, string) (streamDurations, error) { return streamDurations{}, nil }
}

func TestCompositeWithWebcam_WebMComesOutAsMP4(t *testing.T) {
	stubCompositeTools(t)
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	s := &mockStorage{deleteCalled: make(chan string, 4)}

	mock.ExpectExec(`UPDATE videos SET capture_warning`).WithArgs("video-123", pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// Recorded as the MP4 the transcode worker would have produced, so it
	// does not pick the video up and encode it a second time.
	mock.ExpectExec(`UPDATE videos SET status = 'ready', webcam_key = NULL, processing_started_at = NULL, file_key = \$2, content_type = 'video/mp4', file_size = \$3, cues_fixed = true, ios_normalized = true`).
		WithArgs("video-123", "recordings/user/video.mp4", int64(len("mp4 bytes"))).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	CompositeWithWebcam(context.Background(), mock, s, "video-123",
		"recordings/user/video.webm", "recordings/user/video_webcam.webm", "recordings/user/video.jpg", "video/webm")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
	if len(s.uploadFileKeys) == 0 || s.uploadFileKeys[0] != "recordings/user/video.mp4" || s.uploadFileContentTypes[0] != "video/mp4" {
		t.Errorf("want the composite uploaded as recordings/user/video.mp4 video/mp4, got %v %v", s.uploadFileKeys, s.uploadFileContentTypes)
	}
	close(s.deleteCalled)
	var deleted []string
	for k := range s.deleteCalled {
		deleted = append(deleted, k)
	}
	for _, want := range []string{"recordings/user/video_webcam.webm", "recordings/user/video.webm"} {
		if !slices.Contains(deleted, want) {
			t.Errorf("want %s deleted, deleted %v", want, deleted)
		}
	}
}

// An MP4 screen recording keeps its key; only the overlay changes.
func TestCompositeWithWebcam_MP4KeepsItsKey(t *testing.T) {
	stubCompositeTools(t)
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	s := &mockStorage{deleteCalled: make(chan string, 4)}

	mock.ExpectExec(`UPDATE videos SET capture_warning`).WithArgs("video-123", pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE videos SET status = 'ready', webcam_key = NULL, processing_started_at = NULL, updated_at = now\(\) WHERE id = \$1`).
		WithArgs("video-123").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	CompositeWithWebcam(context.Background(), mock, s, "video-123",
		"recordings/user/video.mp4", "recordings/user/video_webcam.mp4", "recordings/user/video.jpg", "video/mp4")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
	if len(s.uploadFileKeys) == 0 || s.uploadFileKeys[0] != "recordings/user/video.mp4" {
		t.Errorf("want the composite at its original key, got %v", s.uploadFileKeys)
	}
	close(s.deleteCalled)
	for k := range s.deleteCalled {
		if k == "recordings/user/video.mp4" {
			t.Error("deleted the composite it just uploaded")
		}
	}
}

// A composite that has to give up publishes the screen alone, and says so on
// the video page rather than leaving the owner to notice the webcam is gone.
// #283.
func TestWebcamDroppedWarning_NamesTheLoss(t *testing.T) {
	if !strings.Contains(webcamDroppedWarning, "webcam") || !strings.Contains(webcamDroppedWarning, "screen") {
		t.Errorf("want the warning to say the webcam was dropped and the screen kept, got %q", webcamDroppedWarning)
	}
}

// The composite deadline grows with the recording, so a long one is not cut
// off at a flat ten minutes, but a bogus duration cannot hold a slot forever.
func TestCompositeTimeout(t *testing.T) {
	for _, c := range []struct {
		seconds int
		want    time.Duration
	}{
		{0, 10 * time.Minute},
		{60, 12 * time.Minute},
		{300, 20 * time.Minute},
		{1 << 30, 10*time.Minute + compositeExtraCapSeconds*time.Second},
		{-5, 10 * time.Minute},
	} {
		if got := compositeTimeout(c.seconds); got != c.want {
			t.Errorf("compositeTimeout(%d) = %v, want %v", c.seconds, got, c.want)
		}
	}
}
