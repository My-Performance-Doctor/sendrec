## Why

MPD-460: Sami decided on 13 Nov 2026 that a video counts as watched once the patient plays 80% of it on their link. SendRec tells MPD only that a play started, after the first second, so mpd-api cannot apply the rule. HQ approved this change with mpd-api's `define-video-watched-rule` on 13 Nov 2026, held until after production Window B (MPD-482).

## What Changes

- After a playback starts, the player reports the stretch it has played to `POST /api/watch/{shareToken}/playback-progress` every few seconds, and when playback pauses or seeks.
- Each playback session keeps a map of the video's seconds it has played. Seconds already played, stretches skipped by a seek, and positions past the end add nothing. A session never holds more seconds than four times the time since its playback started, so bursts cannot fake play and a late report still counts. Once complete, progress only keeps the session open and the player stops reporting.
- Once a session has played 80% of the video's seconds, SendRec enqueues `video.playback_completed` with `playbackSessionId`, `viewerClass`, `watchedSeconds` and `durationSeconds`, once per session.
- Progress keeps the session open for 30 minutes. A session that never started, has expired, or whose media changed is refused.
- Staff previews report no progress and never complete. A video of unknown length never completes.

## Capabilities

### New Capabilities

- `mpd-playback-completion`: per-session play coverage and the completed-play event.

### Modified Capabilities

None.

## Impact

Migration 000078, `internal/mpdevents`, the watch route and player script, producer fixtures and `docs/mpd`. mpd-api accepts the event in its #516; it works before this ships.
