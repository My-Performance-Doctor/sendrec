## Why

MPD-460 counts a video as watched when a patient has seen 80% of it, across every play of the link, not in one sitting. Today `video.playback_completed` fires only when a single playback session reaches 80%, so a patient who watches half on Monday and half on Tuesday never counts as watched. The PEM's 48-hour follow-up also wants the share watched so far, not only a yes or no.

## What Changes

- New table `mpd_video_watch` (migration 000082) holds, per video and media version, the union of the distinct seconds played across all non-staff playback sessions of the link. `mpdevents.Played` updates it in the same transaction as the session's coverage, under the same midpoint and pace rules.
- `video.playback_completed` is sent once per video and media version, when the union reaches 80%. Its fields are unchanged: `playbackSessionId` (the session whose report crossed 80%), `viewerClass`, `watchedSeconds` (the union's seconds) and `durationSeconds`. The per-session completion flag that tells the player to stop reporting stays.
- New event `video.playback_progress` with `watchedSeconds`, `durationSeconds` and `watchedShare` (0 to 1, four decimals), sent each time the union crosses the next 5% step, never backwards. `video.playback_completed` gains `watchedShare`. Both are sent only when `MPD_WATCH_SHARE_ENABLED` is on.
- `MPD_WATCH_SHARE_ENABLED` comes from the CDK context `mpdWatchShare`, `true` or `false`, required whenever the MPD integration is active and refused as `true` while dormant, like `mpdProcessingProgress`. Production mpd-api rejects unknown event types and fields, so it stays off until each environment's mpd-api accepts them.
- The migration backfills links that already completed, so they are not completed again. Earlier partial plays are not carried into the union.
- Contracts, producer fixtures, the MPD README and DEPLOYMENT.md's switch table are updated.

## Impact

- Code: `internal/mpdevents/progress.go`, the video handler and server wiring, `cmd/sendrec/main.go`, `MpdIntegrationConfig.java`.
- Data: one additive table; rollback drops it, and older code ignores it.
- Consumers: mpd-api receives completion across plays with today's fields at once; the new event and field only after the setting is switched per environment.
