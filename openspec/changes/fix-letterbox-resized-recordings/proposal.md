## Why

Staff OS records a browser tab with `MediaRecorder` as H.264 MP4. When the doctor makes the browser full screen mid-take, the captured frame changes size and Chrome restarts its encoder at the new size. SendRec's ffmpeg re-encode sizes its output from each input frame, so when ffmpeg reinitialises its filters at the change the encoder squeezes every later frame into the size it opened with: everything after full screen plays stretched. A WebM recording from Chrome changes size the same way and stretches the same way on conversion. A Chrome MP4 at or below 1080p is also served as uploaded, because nothing in the probe marks it as needing a re-encode, and a mixed-size `avc3` MP4 does not play reliably in Safari.

## What Changes

- Every encode for playback keeps one frame for the whole output: the first frame's size as displayed, fitted within 1920×1080 with its proportions and even dimensions. Later frames of another size are scaled to fit and centred on black, so a full-screen section is letterboxed rather than stretched. This covers the MP4 normalisation, the WebM conversion, trim (of an MP4 or a WebM) and remove-segments. A phone upload that stores its frame with a display rotation gets the turned size, as ffmpeg turns the picture before scaling it.
- An MP4 upload whose video track is tagged `avc3` is re-encoded even when it is otherwise within limits. The tag means the parameter sets travel in-band and the header's size holds for the first frame only, so the file may change size part way through; the re-encode gives it one frame and a plain `avc1` track.
- The WebM conversion, the normalisation, trim and remove-segments run under the composite's deadline, ten minutes plus twice the recording's length, instead of a flat ten minutes: every Staff OS take is re-encoded now, on a two-vCPU host with one encoder slot, and an edit made straight after upload waits for that slot behind the normalisation. The stuck-processing sweep gives every row in processing the same allowance, not only composites.
- The webcam composite and the frame rate are unchanged.

## Capabilities

### New Capabilities

- `resized-recording-playback`: a recording whose frame size changes mid-take plays at one size, with the later section letterboxed.

### Modified Capabilities

None.

## Impact

`internal/video` encode argument builders, the normalisation decision and tests; `docs/mpd/staff-os-handover.md`. Every Staff OS take (all `avc3` once Staff OS switches to in-band parameter sets) is now re-encoded once after upload, under a deadline that grows with its length and the single encoder slot. Nothing changes for files already normalised.

## Approval and delivery

Josh asked for this fix on 2026-10-23. The proposal and the implementation travel in one PR; Sami reviews the proposal before it merges.
