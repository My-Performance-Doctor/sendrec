## 1. One frame per output

- [x] 1.1 Size the output frame from the first frame (fit within 1920×1080, keep proportions, even dimensions) and scale then pad every frame into it in the normalise, WebM conversion, trim and remove-segments encodes; test the fit, the filter and a joined two-size fixture through ffmpeg.
- [x] 1.2 Read the display rotation from the probe and size the frame from the turned sides for a quarter turn; test a rotated fixture through every encoder.
- [x] 1.3 Give a WebM trim the same frame, and check the fit in the resize test by the black bars of the second segment, which a squeeze does not have.

## 2. Re-encode in-band parameter set uploads

- [x] 2.1 Read the video track's codec tag in the probe and treat `avc3` as needing normalisation; test the decision.

## 3. Encode deadline

- [x] 3.1 Run the WebM conversion and the normalisation under the composite's deadline, ten minutes plus twice the recording's length, from the worker and the upload handler; test the deadline each job runs with.
- [x] 3.2 Run trim and remove-segments under the same deadline, and give every row in processing the same allowance in the stuck-processing sweep; test both.
