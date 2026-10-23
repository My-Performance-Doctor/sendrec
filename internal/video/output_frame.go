package video

import (
	"context"
	"fmt"
	"log/slog"
)

// outputFrame is the size every frame of an encoded output is given.
//
// A browser recording changes size when the doctor makes the window full
// screen mid-take: the browser restarts its encoder at the new size and the
// file carries both. ffmpeg reinitialises the filter graph at the change, so a
// scale whose output follows the input (min(1920,iw)) changes size too, and
// the encoder then squeezes every later frame into the size it was opened
// with. Sizing the frame once from the first frame and padding later ones
// keeps their proportions: the full-screen section is letterboxed, not
// stretched.
//
// A zero frame means the size is unknown. The filter then sizes each frame on
// its own, which is what every encode did before.
type outputFrame struct {
	Width  int
	Height int
}

// fitFrame fits width×height within 1920×1080 keeping its proportions, with
// even dimensions as libx264 needs for yuv420p.
func fitFrame(width, height int) outputFrame {
	if width <= 0 || height <= 0 {
		return outputFrame{}
	}
	w, h := width, height
	if w > iOSMaxWidth {
		h = h * iOSMaxWidth / w
		w = iOSMaxWidth
	}
	if h > iOSMaxHeight {
		w = w * iOSMaxHeight / h
		h = iOSMaxHeight
	}
	return outputFrame{Width: w &^ 1, Height: h &^ 1}
}

// filter scales each input frame into the frame and centres it on black.
// force_original_aspect_ratio=decrease with force_divisible_by=2 rounds down,
// so the scaled frame never exceeds the pad, which would fail the encode.
func (f outputFrame) filter() string {
	if f.Width <= 0 || f.Height <= 0 {
		return "scale='min(1920,iw)':'min(1080,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2"
	}
	return fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease:force_divisible_by=2,pad=%d:%d:(ow-iw)/2:(oh-ih)/2",
		f.Width, f.Height, f.Width, f.Height)
}

// probeOutputFrame sizes the output from the input's first frame, which is
// what the container header reports. A failed probe is not an error here: the
// encode goes ahead sizing each frame on its own, as before, rather than
// failing an edit over a header ffmpeg may still decode.
func probeOutputFrame(ctx context.Context, inputPath string) outputFrame {
	props, err := probeVideoProperties(ctx, inputPath)
	if err != nil {
		slog.Warn("encode: could not size the output frame, sizing each frame on its own", "error", err)
		return outputFrame{}
	}
	return fitFrame(props.Width, props.Height)
}
