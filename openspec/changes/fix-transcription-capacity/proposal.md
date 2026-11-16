## Why

Production SendRec started for MPD OS on 16 November 2026. Every Staff OS recording is an `avc3` MP4. Finalizing one re-encodes it and queues a transcript at the same moment. mpd-api creates the patient's report only after the transcript exists.

On 16 November a synthetic 10-minute recording ran on staging tasks the size of production (2 vCPU, 4 GB), using the production encode and Whisper commands:

| Run | Encode | Whisper |
| --- | --- | --- |
| Alone | 7.5 min | 10.1 min |
| Both on one task | 18.5 min | 36.8 min |

When both ran on one task, Whisper overran the fixed 30-minute transcription deadline. A failed transcript is not retried, so that patient never gets a report. Fargate hosts also varied about twofold in speed.

## What Changes

- The transcription deadline grows with the recording. It is 30 minutes plus four times the recording's length, and the added time is capped at 4 hours. The stuck-job reset gives each row the same deadline, so it does not start a long recording a second time while the recording is still inside its time.
- Local Whisper runs at the lowest CPU priority (`nice -n 19`). An encode, and the HTTP server, take the CPU first.
- A worker claims a transcription only when no encode is running on its task. A job claimed beside an encode would spend its deadline waiting.
- A task that is shutting down, as in a deploy or a scale-in, puts its running transcription back in the queue and claims nothing more. Otherwise the job would wait out its whole deadline, now longer, before another task could start it.

Non-goals:

- Larger tasks.
- A hosted transcription provider, which sends patient audio to a third party and needs a privacy decision.
- Retrying failed transcripts.
- How mpd-api treats a failed transcript. It waits in `waiting_captions` until `video.transcript_ready` arrives.

## Capabilities

### New Capabilities

- `transcription-capacity`: how long a transcription may run, and how it shares a task's CPU with encodes.

### Modified Capabilities

None.

## Impact

The change touches `internal/video/transcribe_worker.go`, `transcribe.go`, `transcriber_local.go`, `encoder_slots.go` and their tests. It has no migration, no infrastructure change and no new configuration. Production changes only with a reviewed production release.

## Approval and delivery

Josh asked for this change on 16 November 2026, before the 17 November go-live. Sami reviews this proposal and its implementation together in one pull request.
