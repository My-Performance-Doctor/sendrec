## ADDED Requirements

### Requirement: The MPD integration reports processing progress
While `MPD_PROCESSING_PROGRESS_ENABLED` is true, `GET /api/integrations/mpd/videos/{videoId}` SHALL return `processing` with `conversion` and `transcript`, each carrying `state`, `percent` (a whole number from 0 to 100, or null) and `failure` (a short code, or null), and `updatedAt`. Conversion SHALL be `not_needed` for an upload that needs no conversion, `pending` before ffmpeg starts, `running` while it runs or waits for another attempt, `done` when the MP4 is published, and `failed` only when no attempt remains or the error is permanent. Transcript SHALL be `pending`, `running`, `done`, `no_audio` or `failed`. No raw error text SHALL be returned.

#### Scenario: The setting is off
- **WHEN** `MPD_PROCESSING_PROGRESS_ENABLED` is unset or false
- **THEN** the metadata response has exactly the fields it has today, and no `processing`

#### Scenario: A conversion halfway through
- **WHEN** ffmpeg has written 300 of a 600-second recording
- **THEN** `processing.conversion` reads `running` with `percent` 50

#### Scenario: Amazon Transcribe
- **WHEN** an Amazon Transcribe job is in progress
- **THEN** `processing.transcript` reads `running` with `percent` null

#### Scenario: A conversion SendRec will try again
- **WHEN** an attempt fails and attempts remain
- **THEN** `processing.conversion` reads `running`, not `failed`

### Requirement: MPD can restart a failed step
`POST /api/integrations/mpd/videos/{videoId}/retry` with `step` and `mediaVersion` SHALL restart a failed conversion by resetting its attempts and queueing it again, or a failed transcript as the app's Retry transcript does, and answer 202 with `processing`. A step that has not failed, or a `mediaVersion` that is not the current one, SHALL be refused with 409 `step_not_failed` and change nothing. An unknown video, or one outside the integration's tenant and workspace, SHALL answer 404.

#### Scenario: Retry a killed transcript
- **WHEN** the transcript is `failed` and MPD asks to retry it
- **THEN** it reads `pending`, then `running`, and `video.transcript_ready` follows when it is done

#### Scenario: Retry a step that did not fail
- **WHEN** MPD asks to retry a conversion that is `running`
- **THEN** the answer is 409 `step_not_failed` and nothing changes
