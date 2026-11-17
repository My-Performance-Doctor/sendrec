package video

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v5"
)

func TestScanProgressReadsOutTimeInMicroseconds(t *testing.T) {
	var seen []int64
	scanProgress(strings.NewReader("frame=10\nout_time_us=1500000\nout_time_ms=3000000\nout_time=00:00:03\nout_time_us=N/A\nprogress=continue\n"),
		func(m int64) { seen = append(seen, m) })
	if len(seen) != 2 || seen[0] != 1_500_000 || seen[1] != 3_000_000 {
		t.Fatalf("got %v", seen)
	}
}

func TestConversionReporterWritesWholePercentsEveryTwoSecondsAndStopsAt99(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	now := time.Unix(0, 0)
	report := conversionReporter(context.Background(), mock, "video", "key.webm", 600, func() time.Time { return now })
	mock.ExpectExec("UPDATE videos SET conversion_percent").WithArgs("video", "key.webm", 50).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	report(300_000_000) // 50% at t=0: written
	now = now.Add(time.Second)
	report(306_000_000) // 51% one second later: too soon
	now = now.Add(2 * time.Second)
	mock.ExpectExec("UPDATE videos SET conversion_percent").WithArgs("video", "key.webm", 99).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	report(700_000_000) // past the end: held at 99 until published
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestConversionReporterWithoutADurationWritesNothing(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	conversionReporter(context.Background(), mock, "video", "key", 0, time.Now)(5_000_000)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessingStepsMapToMPDStates(t *testing.T) {
	percent := 40
	for _, c := range []struct {
		name string
		row  processingRow
		want string
	}{
		{"mp4 already normalized", processingRow{status: "ready", contentType: "video/mp4", iosNormalized: true}, "done"},
		{"webm waiting", processingRow{status: "ready", contentType: "video/webm"}, "pending"},
		{"webm encoding", processingRow{status: "ready", contentType: "video/webm", encoding: true, percent: &percent}, "running"},
		{"webm between attempts", processingRow{status: "ready", contentType: "video/webm", attempts: 2}, "running"},
		{"webm out of attempts", processingRow{status: "ready", contentType: "video/webm", attempts: maxTranscodeAttempts}, "failed"},
		{"webcam composite", processingRow{status: "processing", contentType: "video/webm"}, "running"},
	} {
		got := conversionStep(c.row)
		if got.State != c.want {
			t.Fatalf("%s: got %s", c.name, got.State)
		}
		if c.want == "failed" && (got.Failure == nil || *got.Failure != "conversion_failed") {
			t.Fatalf("%s: failure %v", c.name, got.Failure)
		}
	}
	for status, want := range map[string]string{"none": "pending", "pending": "pending", "processing": "running", "ready": "done", "no_audio": "no_audio", "failed": "failed"} {
		if got := transcriptStep(processingRow{transcript: status}).State; got != want {
			t.Fatalf("transcript %s: got %s", status, got)
		}
	}
}
