## ADDED Requirements

### Requirement: Links open a video at a moment
The watch and embed pages SHALL start playback at the time given by the `t` query parameter when it is valid and before the end of the video.

#### Scenario: Time in seconds
- **WHEN** a viewer opens `/watch/{token}?t=83` for a video longer than 83 seconds
- **THEN** the player starts at 83 seconds

#### Scenario: Time in minutes and seconds
- **WHEN** the link carries `t=1:23` or `t=1m23s`
- **THEN** the player starts at 83 seconds

#### Scenario: Time in hours, minutes and seconds
- **WHEN** the link carries `t=1:02:03` or `t=1h2m3s` for a video longer than that
- **THEN** the player starts at 3,723 seconds

#### Scenario: Time past the end or not a time
- **WHEN** the time is past the end of the video or does not parse
- **THEN** the player starts at the beginning

#### Scenario: Password-protected video
- **WHEN** a viewer opens a password-protected video with `t` and enters the right password
- **THEN** the page reloads with the same address and the player starts at that time

### Requirement: Transcript times can be played from the keyboard
Each time beside a transcript line or chapter on the watch page SHALL be a button labelled with the time it plays from.

#### Scenario: Keyboard
- **WHEN** a viewer moves focus to a transcript time and presses Enter or Space
- **THEN** the player moves to that line's start and plays, and the time shows a focus ring while focused
