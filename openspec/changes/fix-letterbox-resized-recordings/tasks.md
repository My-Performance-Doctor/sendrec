## 1. One frame per output

- [x] 1.1 Size the output frame from the first frame (fit within 1920×1080, keep proportions, even dimensions) and scale then pad every frame into it in the normalise, WebM conversion, trim and remove-segments encodes; test the fit, the filter and a joined two-size fixture through ffmpeg.

## 2. Re-encode in-band parameter set uploads

- [x] 2.1 Read the video track's codec tag in the probe and treat `avc3` as needing normalisation; test the decision.
