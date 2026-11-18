## ADDED Requirements

### Requirement: Completion counts every play of the link

SendRec SHALL keep, per video and media version, the union of the distinct seconds played by every non-staff playback session, under the session coverage rules. It SHALL enqueue one `video.playback_completed` event per video and media version when the union reaches 80%, from the session whose report crossed it, with `watchedSeconds` as the union's seconds.

#### Scenario: Two plays over different halves
- **WHEN** one session plays seconds 0 to 45 and another plays seconds 50 to 95 of a 100-second video
- **THEN** one `video.playback_completed` event is enqueued, from the second session, and the union holds 90 seconds

#### Scenario: The same seconds twice
- **WHEN** two sessions each play seconds 0 to 45 of a 100-second video
- **THEN** the union holds 45 seconds and no completed event is enqueued

#### Scenario: A staff preview
- **WHEN** a staff preview plays the whole video and a patient session plays 45%
- **THEN** the union holds 45 seconds and no completed event is enqueued

#### Scenario: Completion is sent once
- **WHEN** a further session plays the whole video after the link completed
- **THEN** no second completed event is enqueued for that media version

#### Scenario: A new media version
- **WHEN** the video is replaced with a new media version after completing
- **THEN** the new version's union starts empty and completes on its own plays

### Requirement: Watched-share steps behind a deploy setting

When `MPD_WATCH_SHARE_ENABLED` is on, SendRec SHALL enqueue `video.playback_progress` with `watchedSeconds`, `durationSeconds` and `watchedShare` (0 to 1, four decimals) each time the union crosses a 5% step above the last one reported, never for a lower step, and SHALL add `watchedShare` to `video.playback_completed`. When it is off, SendRec SHALL send neither and SHALL not advance the reported step.

#### Scenario: Setting on
- **WHEN** the setting is on and the union grows from 12% to 33%
- **THEN** progress events are enqueued at increasing shares, the last at 30% or more, and replaying covered seconds enqueues none

#### Scenario: Setting off
- **WHEN** the setting is off
- **THEN** no `video.playback_progress` event is enqueued and `video.playback_completed` carries no `watchedShare`

### Requirement: The setting is a required deploy context

The CDK stack SHALL set `MPD_WATCH_SHARE_ENABLED` from the context `mpdWatchShare`, which SHALL be `true` or `false` whenever the MPD integration is active, and SHALL refuse `true` while the integration is dormant.

#### Scenario: Missing while active
- **WHEN** an active deploy leaves out `mpdWatchShare`
- **THEN** synthesis fails naming the setting
