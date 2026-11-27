## Why

A finalized recording's transcript waits on three fixed delays before the transcription itself:

- The transcription worker polls for pending jobs every 5 seconds. A job enqueued just after a poll waits up to 5 seconds before it starts.
- Each task runs one transcription at a time. With a remote provider a job takes about 20 to 30 seconds, most of it waiting on the provider, so in a burst of recordings each job also waits for every job ahead of it.
- The Amazon Transcribe provider checks a running batch job every 3 seconds, which adds about 1.5 seconds on average to a job that usually finishes in 10 to 20 seconds.

## What Changes

- A transcription job starts as soon as it is queued. Enqueueing a job wakes this process's worker. A finishing job also wakes it, so the next pending job starts at once. The 5-second poll stays as the fallback for jobs queued on another task or left by a stopped one.
- A worker runs up to `MAX_CONCURRENT_TRANSCRIPTIONS` jobs at once (default 3). Each job is claimed as before, one row at a time with `FOR UPDATE SKIP LOCKED`, so no two workers claim the same job. Local Whisper is CPU bound and still runs one job at a time.
- When a remote provider's job falls back to local Whisper, Whisper runs one job at a time in the process. While Whisper is busy, the worker claims no further job beside the ones it is running, so jobs do not spend their deadlines waiting for it.
- At shutdown, every running job is handed back to the queue, as the one running job was before.
- The Amazon Transcribe provider checks a job every second for its first 30 seconds, then every 3 seconds as before.

Non-goals:

- Streaming or live transcription.
- A different transcription provider, or a change to which provider is enabled.
- Changes to the upload flow.
- Waking workers on other tasks. Their poll finds the job within 5 seconds.

## Capabilities

### New Capabilities

- `transcription-latency`: when a queued transcription starts, how many run at once, and how often a remote job is checked.

### Modified Capabilities

None.

## Impact

The change touches `internal/video/transcribe_worker.go`, `transcriber_local.go`, `transcriber_aws.go`, `cmd/sendrec/main.go` and their tests. It adds the optional `MAX_CONCURRENT_TRANSCRIPTIONS` setting. It has no migration and no infrastructure change.

With an idle queue a transcript is ready about 2 to 3 seconds sooner on average. In a burst, a job no longer waits about 20 to 30 seconds for each job ahead of it, up to the concurrency limit. Up to three remote jobs per task check their status once a second during their first 30 seconds, well inside Amazon Transcribe's request quota.

## Approval and delivery

The change was approved on 27 November 2026. The proposal and its implementation are reviewed together in one pull request.
