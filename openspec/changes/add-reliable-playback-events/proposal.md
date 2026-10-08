## Why

MPD needs processing and playback events that survive a task restart. Current video.viewed notifications count page access, and webhook delivery has only three in-memory attempts.

## What Changes

- Add a versioned, durable MPD event stream alongside existing user notification webhooks.
- Distinguish page access from first actual playback; retain progress milestones and label unidentified viewers honestly.
- Persist events with stable identifiers, retry after restart, alert on failures and support controlled replay.

## Capabilities

### New Capabilities

- `reliable-playback-events`: Add a versioned, durable MPD event stream alongside existing user notification webhooks.

### Modified Capabilities

None in the current main spec store. Integration behavior is defined by the new capability; existing provider-specific requirements remain in force.

## Impact

Go playback handlers and native players, webhook storage/worker, delivery configuration. Reuses existing video processing and player code. The consumer is mpd-api `add-sendrec-video-reports`; staff identity comes from `add-mpd-staff-sign-in`. No patient matching, patient metadata fields or replacement player is included.

## Approval and delivery

This proposal is one of five changes requested on 2026-10-08. Proposal review and explicit apply authorization precede implementation. Each implementation change lands through a human-merged PR. All examples and tests use synthetic identities and media.
