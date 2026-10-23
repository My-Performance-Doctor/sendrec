package video

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestFitFrame(t *testing.T) {
	tests := []struct {
		name          string
		width, height int
		want          outputFrame
	}{
		{"within limits unchanged", 1500, 1018, outputFrame{1500, 1018}},
		{"odd dimensions rounded down to even", 1501, 1019, outputFrame{1500, 1018}},
		{"wider than 1920", 2400, 892, outputFrame{1920, 712}},
		{"taller than 1080", 641, 1281, outputFrame{540, 1080}},
		{"both over, high-DPI capture", 3243, 2627, outputFrame{1332, 1080}},
		{"exactly at limits", 1920, 1080, outputFrame{1920, 1080}},
		{"unknown size", 0, 0, outputFrame{}},
		{"negative size", -1, 720, outputFrame{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fitFrame(tt.width, tt.height); got != tt.want {
				t.Errorf("fitFrame(%d, %d) = %+v, want %+v", tt.width, tt.height, got, tt.want)
			}
		})
	}
}

func TestOutputFrameFilter(t *testing.T) {
	t.Run("fixed frame scales then pads to the same size", func(t *testing.T) {
		got := outputFrame{1500, 1018}.filter()
		want := "scale=1500:1018:force_original_aspect_ratio=decrease:force_divisible_by=2,pad=1500:1018:(ow-iw)/2:(oh-ih)/2"
		if got != want {
			t.Errorf("filter() = %q, want %q", got, want)
		}
	})
	t.Run("unknown frame sizes each frame on its own", func(t *testing.T) {
		got := outputFrame{}.filter()
		if strings.Contains(got, "pad=") || !strings.Contains(got, "min(1920,iw)") {
			t.Errorf("filter() = %q, want the per-frame scale without a pad", got)
		}
	})
}

// A recording whose frame size changes part way through must come out at one
// size, the first frame's, with every later frame fitted inside it. The
// fixture is two MPEG-TS segments of different sizes joined end to end:
// MPEG-TS carries the parameter sets in-band, so ffmpeg decodes the change
// the way it decodes a browser recording's.
func TestEncodeKeepsOneFrameSizeAcrossAResize(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("ffmpeg missing in CI; the resize check cannot be skipped here")
		}
		t.Skip("ffmpeg not installed locally")
	}

	dir := t.TempDir()
	segments := []string{dir + "/a.ts", dir + "/b.ts"}
	for i, size := range []string{"320x240", "480x200"} {
		mk := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
			"-f", "lavfi", "-i", "testsrc2=size="+size+":rate=10:duration=1",
			"-c:v", "libx264", "-pix_fmt", "yuv420p", "-f", "mpegts", "-y", segments[i])
		if out, err := mk.CombinedOutput(); err != nil {
			t.Skipf("cannot build fixture: %v: %s", err, out)
		}
	}
	var joined []byte
	for _, seg := range segments {
		data, err := os.ReadFile(seg)
		if err != nil {
			t.Fatal(err)
		}
		joined = append(joined, data...)
	}
	input := dir + "/resized.ts"
	if err := os.WriteFile(input, joined, 0o600); err != nil {
		t.Fatal(err)
	}

	frameSizes := func(t *testing.T, path string) map[string]int {
		t.Helper()
		out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0",
			"-show_entries", "frame=width,height", "-of", "csv=p=0", path).Output()
		if err != nil {
			t.Fatalf("ffprobe: %v", err)
		}
		sizes := map[string]int{}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			sizes[strings.TrimSuffix(line, ",")]++
		}
		return sizes
	}
	if got := frameSizes(t, input); len(got) != 2 {
		t.Fatalf("fixture has frame sizes %v, want two", got)
	}

	ctx := context.Background()
	for _, tc := range []struct {
		name string
		run  func(output string) error
	}{
		{"transcode", func(output string) error { return transcodeToMP4(ctx, input, output, "") }},
		{"normalize", func(output string) error { return transcodeToIOSCompatible(ctx, input, output, "") }},
		{"trim", func(output string) error { return trimVideo(ctx, input, output, "video/mp4", 0.5, 1.5) }},
		{"remove segments", func(output string) error {
			return removeSegmentsFromVideo(ctx, input, output, "video/mp4", []segmentRange{{Start: 0.5, End: 0.7}}, false)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := dir + "/" + strings.ReplaceAll(tc.name, " ", "-") + ".mp4"
			if err := tc.run(output); err != nil {
				t.Fatalf("encode failed: %v", err)
			}
			got := frameSizes(t, output)
			if len(got) != 1 || got["320,240"] == 0 {
				t.Errorf("output frame sizes %v, want only 320,240", got)
			}
		})
	}
}
