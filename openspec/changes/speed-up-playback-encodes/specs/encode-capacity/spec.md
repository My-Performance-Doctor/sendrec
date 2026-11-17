## ADDED Requirements

### Requirement: One conversion per video at a time
A WebM transcode or MP4 normalize SHALL take the video's encode lease before it downloads the recording, and SHALL skip the video while another conversion holds a live lease. A lease SHALL stay live for the encode deadline of the video's duration plus one minute. A failed or published conversion SHALL release its lease.

#### Scenario: Sweep during a running normalize
- **WHEN** a normalize started at finalize is still running after 5 minutes, and the sweep on either task finds the video
- **THEN** the sweep skips it and the recording is encoded once

#### Scenario: Task stopped mid-encode
- **WHEN** the task running a conversion receives a stop signal
- **THEN** it releases that conversion's lease before draining HTTP, claims no new conversion, and the next sweep on another task converts the video

#### Scenario: Task killed without a release
- **WHEN** the task running a conversion dies without releasing its lease, and the lease's deadline plus one minute passes
- **THEN** the next sweep converts the video

#### Scenario: Edit during a conversion
- **WHEN** an owner edits a recording while a conversion of its earlier file runs
- **THEN** the edit clears the lease and the edited file can be claimed at once

#### Scenario: Failure from a stale holder
- **WHEN** a conversion whose lease has expired and been taken by another conversion fails
- **THEN** it counts no attempt and leaves the newer lease alone

#### Scenario: Failed conversion
- **WHEN** a conversion fails within its attempt budget
- **THEN** its lease is released and the next sweep may retry it

### Requirement: Playback encodes keep the source frame rate
Normalize, WebM transcode, MP4 trim and webcam composite SHALL encode H.264 with the `veryfast` preset at the source's frame rate, capped at 60 frames a second.

#### Scenario: 30 fps Staff OS recording
- **WHEN** a 30 fps recording is normalized
- **THEN** the output has 30 frames a second, not 60

#### Scenario: High frame rate upload
- **WHEN** a 120 fps upload is normalized
- **THEN** the output has 60 frames a second
