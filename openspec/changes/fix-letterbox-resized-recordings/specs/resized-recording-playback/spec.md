## ADDED Requirements

### Requirement: One frame size per encoded output
Every encode of a recording for playback other than the webcam composite SHALL output one frame size for its whole length: the first frame's size as displayed, fitted within 1920×1080 with its proportions kept and both dimensions even. Each later frame SHALL be scaled to fit inside that frame and centred on black.

#### Scenario: Full screen mid-take
- **WHEN** a recording's frame size changes part way through
- **THEN** the converted file has one frame size and the section after the change keeps its proportions with bars on the sides that do not fit

#### Scenario: Rotated phone upload
- **WHEN** the input stores a landscape frame with a quarter-turn display rotation
- **THEN** the output frame is the turned, portrait size and the picture fills it

#### Scenario: Unknown first frame
- **WHEN** the input's size cannot be read before the encode
- **THEN** the encode goes ahead sizing each frame on its own, as before

### Requirement: In-band parameter set uploads are normalised
An MP4 upload whose video track is tagged `avc3` SHALL be re-encoded for playback even when its size, level and frame rate are within limits.

#### Scenario: Chrome take within limits
- **WHEN** a 1080p or smaller `avc3` MP4 is uploaded
- **THEN** it is re-encoded to a single-size `avc1` file before it is marked normalised

### Requirement: Encode deadlines grow with the recording
The WebM conversion, the normalisation, trim and remove-segments SHALL run under a deadline of ten minutes plus twice the recording's length, capped as the webcam composite's deadline is, whether the worker, the upload handler or an edit request starts them. The stuck-processing sweep SHALL wait the same extra time for every row in processing before resetting it.

#### Scenario: Long consult take
- **WHEN** a 20 minute recording is converted
- **THEN** the job has 50 minutes before it is counted as a failed attempt

#### Scenario: Trim straight after upload
- **WHEN** a 20 minute take is trimmed while its normalisation holds the encoder slot
- **THEN** the trim has 50 minutes, and the sweep leaves the row in processing until that deadline has passed

#### Scenario: Length not yet known
- **WHEN** the recording's duration is zero because it has not been probed
- **THEN** the job has the ten minute base deadline
