## ADDED Requirements

### Requirement: A session reports completion at 80% of the video

SendRec SHALL enqueue one `video.playback_completed` event for a non-preview playback session once the distinct seconds of the video it has played reach 80% of the video's length, carrying `playbackSessionId`, `viewerClass`, `watchedSeconds` and `durationSeconds`.

#### Scenario: A viewer plays most of the video
- **WHEN** a session plays 80 of a 100-second video's seconds
- **THEN** one `video.playback_completed` event is enqueued for it

#### Scenario: A staff preview
- **WHEN** a staff preview plays the whole video
- **THEN** no completed event is enqueued

### Requirement: Only played seconds count

A session's coverage SHALL count each second of the video once. Seconds skipped by a seek, seconds past the video's end, and observations faster than four times the server's clock SHALL add nothing.

#### Scenario: A seek to the end
- **WHEN** a session plays 10 seconds, seeks to 90 and plays to the end of a 100-second video
- **THEN** its coverage is 20 seconds and no completed event is enqueued

#### Scenario: A rewatch
- **WHEN** a session plays the first 40 seconds twice
- **THEN** its coverage is 40 seconds

### Requirement: Progress needs a started, open session

Progress SHALL be refused for a session with no playback-start fact, an expired session, or one whose media changed. Accepted progress SHALL keep the session open for 30 minutes.

#### Scenario: Progress without a start
- **WHEN** progress arrives for a session that never sent its playback start
- **THEN** it is refused and nothing is recorded
