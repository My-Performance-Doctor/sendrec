package video

import (
	"context"
	"fmt"
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

// The header stores a phone's landscape sensor size with a display rotation;
// ffmpeg turns the picture upright before the filter, so the frame must be
// the turned size or portrait content ends up pillarboxed in a landscape frame.
func TestProbeOutputFrameFollowsTheDisplayRotation(t *testing.T) {
	original := probeVideoProperties
	t.Cleanup(func() { probeVideoProperties = original })

	for _, tt := range []struct {
		rotation int
		want     outputFrame
	}{
		{0, outputFrame{640, 360}},
		{90, outputFrame{360, 640}},
		{-90, outputFrame{360, 640}},
		{270, outputFrame{360, 640}},
		{180, outputFrame{640, 360}},
		{-180, outputFrame{640, 360}},
	} {
		probeVideoProperties = func(context.Context, string) (videoProperties, error) {
			return videoProperties{Width: 640, Height: 360, Rotation: tt.rotation}, nil
		}
		if got := probeOutputFrame(context.Background(), "in.mp4"); got != tt.want {
			t.Errorf("rotation %d: frame %+v, want %+v", tt.rotation, got, tt.want)
		}
	}
}

// requireFFmpeg skips locally without ffmpeg and fails in CI, which installs
// it so that these checks run.
func requireFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("ffmpeg missing in CI; the encode checks cannot be skipped here")
		}
		t.Skip("ffmpeg not installed locally")
	}
}

// ffmpegFixture runs ffmpeg to build a test input. It skips if it cannot,
// except in CI, where a skip would pass the encode checks without running them.
func ffmpegFixture(t *testing.T, args ...string) {
	t.Helper()
	mk := exec.Command("ffmpeg", append([]string{"-nostdin", "-loglevel", "error"}, args...)...)
	if out, err := mk.CombinedOutput(); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("cannot build fixture in CI: %v: %s", err, out)
		}
		t.Skipf("cannot build fixture: %v: %s", err, out)
	}
}

// frameSizes counts the decoded frames of each size in the file.
func frameSizes(t *testing.T, path string) map[string]int {
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

// topRowsLuma is the mean luma of the top rows of the frame at the given time:
// near 16 (video black) where a letterbox bar is, far above it over picture.
func topRowsLuma(t *testing.T, path string, at float64, rows int) float64 {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-ss", fmt.Sprintf("%.3f", at), "-i", path, "-frames:v", "1",
		"-vf", fmt.Sprintf("crop=iw:%d:0:0", rows), "-f", "rawvideo", "-pix_fmt", "gray", "-").Output()
	if err != nil || len(out) == 0 {
		t.Fatalf("ffmpeg frame read at %.3fs: %v (%d bytes)", at, err, len(out))
	}
	var sum int
	for _, b := range out {
		sum += int(b)
	}
	return float64(sum) / float64(len(out))
}

// A recording whose frame size changes part way through must come out at one
// size, the first frame's, with every later frame fitted inside it. The
// fixture is two MPEG-TS segments of different sizes joined end to end:
// MPEG-TS carries the parameter sets in-band, so ffmpeg decodes the change
// the way it decodes a browser recording's.
//
// One output size alone does not prove the fit: ffmpeg's autoscale also gives
// one size, by squeezing the later frames. The second segment is wider than
// the first, so fitted frames carry black bars top and bottom where squeezed
// ones carry picture; the top rows of a frame from each segment tell them
// apart.
func TestEncodeKeepsOneFrameSizeAcrossAResize(t *testing.T) {
	requireFFmpeg(t)

	dir := t.TempDir()
	segments := []string{dir + "/a.ts", dir + "/b.ts"}
	for i, size := range []string{"320x240", "480x200"} {
		ffmpegFixture(t, "-f", "lavfi", "-i", "testsrc2=size="+size+":rate=10:duration=1",
			"-c:v", "libx264", "-pix_fmt", "yuv420p", "-f", "mpegts", "-y", segments[i])
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
	if got := frameSizes(t, input); len(got) != 2 {
		t.Fatalf("fixture has frame sizes %v, want two", got)
	}

	// A 480x200 frame fitted in 320x240 is 320x132 with 54 black rows above it.
	const barRows = 40
	const barLumaMax = 32

	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		output string
		run    func(output string) error
		// Times in the output that fall in the first and the second segment. The
		// joined fixture restarts its clock at the join, so remove-segments keeps
		// less of it than the cut suggests.
		first, second float64
	}{
		{"transcode", "transcode.mp4", func(output string) error { return transcodeToMP4(ctx, input, output, "") }, 0.2, 1.5},
		{"normalize", "normalize.mp4", func(output string) error { return transcodeToIOSCompatible(ctx, input, output, "") }, 0.2, 1.5},
		{"trim", "trim.mp4", func(output string) error { return trimVideo(ctx, input, output, "video/mp4", 0.5, 1.5) }, 0.2, 0.75},
		{"trim webm", "trim.webm", func(output string) error { return trimVideo(ctx, input, output, "video/webm", 0.5, 1.5) }, 0.2, 0.75},
		{"remove segments", "removed.mp4", func(output string) error {
			return removeSegmentsFromVideo(ctx, input, output, "video/mp4", []segmentRange{{Start: 0.5, End: 0.7}}, false)
		}, 0.2, 0.75},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := dir + "/" + tc.output
			if err := tc.run(output); err != nil {
				t.Fatalf("encode failed: %v", err)
			}
			got := frameSizes(t, output)
			if len(got) != 1 || got["320,240"] == 0 {
				t.Errorf("output frame sizes %v, want only 320,240", got)
			}
			if luma := topRowsLuma(t, output, tc.first, barRows); luma <= barLumaMax {
				t.Errorf("top rows at %.2fs have mean luma %.0f; the first segment should fill the frame", tc.first, luma)
			}
			if luma := topRowsLuma(t, output, tc.second, barRows); luma > barLumaMax {
				t.Errorf("top rows at %.2fs have mean luma %.0f, want a black bar (at most %d): the second segment was squeezed, not fitted", tc.second, luma, barLumaMax)
			}
		})
	}
}

// A phone recording stores its sensor size and a display rotation. ffmpeg
// turns it upright before the filter, so the output must be the turned size:
// 360x640 from a 640x360 file turned a quarter, not 640x360 with the portrait
// picture pillarboxed.
func TestEncodeKeepsARotatedInputUpright(t *testing.T) {
	requireFFmpeg(t)

	dir := t.TempDir()
	flat := dir + "/flat.mp4"
	input := dir + "/rotated.mp4"
	ffmpegFixture(t, "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=10:duration=1",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-y", flat)
	ffmpegFixture(t, "-display_rotation", "90", "-i", flat, "-c", "copy", "-y", input)

	props, err := probeVideoProperties(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if props.Width != 640 || props.Height != 360 || props.Rotation%180 == 0 {
		t.Fatalf("fixture probes as %dx%d rotation %d, want 640x360 turned a quarter", props.Width, props.Height, props.Rotation)
	}

	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		output string
		run    func(output string) error
	}{
		{"transcode", "transcode.mp4", func(output string) error { return transcodeToMP4(ctx, input, output, "") }},
		{"normalize", "normalize.mp4", func(output string) error { return transcodeToIOSCompatible(ctx, input, output, "") }},
		{"trim", "trim.mp4", func(output string) error { return trimVideo(ctx, input, output, "video/mp4", 0.2, 0.8) }},
		{"remove segments", "removed.mp4", func(output string) error {
			return removeSegmentsFromVideo(ctx, input, output, "video/mp4", []segmentRange{{Start: 0.2, End: 0.4}}, false)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := dir + "/" + tc.output
			if err := tc.run(output); err != nil {
				t.Fatalf("encode failed: %v", err)
			}
			if got := frameSizes(t, output); len(got) != 1 || got["360,640"] == 0 {
				t.Errorf("output frame sizes %v, want only 360,640", got)
			}
		})
	}
}
