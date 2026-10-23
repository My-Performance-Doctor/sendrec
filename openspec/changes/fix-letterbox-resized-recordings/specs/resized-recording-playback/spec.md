## ADDED Requirements

### Requirement: One frame size per encoded output
Every encode that scales a recording for playback SHALL output one frame size for its whole length: the first frame's size fitted within 1920×1080 with its proportions kept and both dimensions even. Each later frame SHALL be scaled to fit inside that frame and centred on black.

#### Scenario: Full screen mid-take
- **WHEN** a recording's frame size changes part way through
- **THEN** the converted file has one frame size and the section after the change keeps its proportions with bars on the sides that do not fit

#### Scenario: Unknown first frame
- **WHEN** the input's size cannot be read before the encode
- **THEN** the encode goes ahead sizing each frame on its own, as before

### Requirement: In-band parameter set uploads are normalised
An MP4 upload whose video track is tagged `avc3` SHALL be re-encoded for playback even when its size, level and frame rate are within limits.

#### Scenario: Chrome take within limits
- **WHEN** a 1080p or smaller `avc3` MP4 is uploaded
- **THEN** it is re-encoded to a single-size `avc1` file before it is marked normalised
