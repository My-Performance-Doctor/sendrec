## Why

Staff OS shows a percentage only while a recording uploads. After that it says "Processing", sometimes for 30 minutes or more, because SendRec's MPD integration reports `processing`, `ready` or `deleted` and nothing else. On 16 Nov 2026 a transcript was killed. Only a Practice Admin pressing **Retry transcript** in SendRec's own app recovered it; the doctor's **Try again** in Staff OS waited on the same stuck transcript. Sami asked for a real percentage and a retry like SendRec's (MPD-498).

## What Changes

- ffmpeg's conversion of a WebM recording to MP4 runs with `-progress pipe:1`. The worker writes `conversion_percent`, from `out_time_us` against the probed duration, at most every 2 seconds and only when the whole percent changes.
- The transcript reports its state and no percent. MPD now uses Amazon Transcribe (`add-aws-transcription`), which gives job state only and runs in seconds; a local whisper-cli percent is left out.
- `GET /api/integrations/mpd/videos/{videoId}` gains `processing`: `conversion` and `transcript`, each with `state`, `percent` and a short `failure` code, and `updatedAt`.
  - Conversion is `not_needed` for an MP4 or QuickTime upload, and `failed` only after the last attempt or a permanent ffmpeg error. While SendRec retries it is `running`.
  - Transcript maps `pending` and `processing` to `pending` and `running`, `ready` to `done`, and keeps `no_audio` and `failed`.
  - Raw error text never leaves SendRec.
- `POST /api/integrations/mpd/videos/{videoId}/retry` with `{ "step": "conversion" | "transcript", "mediaVersion" }` restarts only a failed step, then answers 202 with `processing`.
  - **Conversion:** resets its attempts and error, and queues the transcode job again.
  - **Transcript:** takes the same path as the app's Retry transcript, and answers 503 when transcription is off on the server, as that button refuses.
  - **Refusals:** a step that has not failed, or a media version that moved, answers 409 `step_not_failed`. An unknown video answers 404.
  - The route uses the integration's existing service authentication and tenant and workspace check.
- **Behind a setting, off by default.** `MPD_PROCESSING_PROGRESS_ENABLED` (default `false`) decides whether the metadata response carries `processing`. mpd-api before #559 refuses metadata with a field it does not know, and refusing it stops report processing. So the field stays off in an environment until the mpd-api it calls runs #559 (staging on staging mpd-api, production on production mpd-api), and turning the setting on is a separate, recorded step. Deploys set it through the required CDK setting `mpdProcessingProgress` whenever the MPD integration is active. Recording the percents and the retry route are not behind the setting: neither changes a response an older mpd-api reads.
- Migration 000079: `conversion_percent` on `videos`, nullable, 0 to 100. `updatedAt` is the row's own `updated_at`.

## Capabilities

### New Capabilities

- `mpd-processing-progress`: what SendRec tells MPD about a recording's processing, and how MPD restarts a failed step.

### Modified Capabilities

None.

## Impact

- **Code:** `internal/video/encode_progress.go` and `mpd_processing.go` (new), `transcode.go`, `normalize.go`, `mpd_adapter.go`, `server_mpd.go`, `cmd/sendrec/main.go`, CDK's `MpdIntegrationConfig`, migration 000079 and their tests.
- **Callers:** none change. mpd-api reads `processing` and calls retry in its own change. A caller that ignores the new field sees today's response.
- **Sequencing:** built on main after #36 (`add-aws-transcription`) merges, because both change the transcribe worker.

## Approval and delivery

The contract is on MPD-498. Per this repository's rule, Sami reviews the proposal before it is applied.
