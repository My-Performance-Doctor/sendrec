## ADDED Requirements

### Requirement: A queued transcription starts at once
Enqueueing a transcription SHALL wake the transcription worker in the same process, and a finishing transcription SHALL wake it too. The worker SHALL still poll for pending jobs every 5 seconds.

#### Scenario: Recording finalized on an idle task
- **WHEN** a recording is finalized and its transcription is queued
- **THEN** the task's worker claims the job without waiting for its next poll

#### Scenario: Job queued on another task
- **WHEN** a job is queued on a task whose worker is busy or stopped
- **THEN** a worker on another task claims it at its next poll

### Requirement: Transcriptions run concurrently
A transcription worker SHALL run up to `MAX_CONCURRENT_TRANSCRIPTIONS` jobs at once, 3 when unset. Local Whisper SHALL run one job at a time. Each job SHALL be claimed by exactly one worker. At shutdown a task SHALL hand every running job back to the queue.

#### Scenario: Burst of recordings
- **WHEN** four recordings are queued at once on a task with a remote provider and the default limit
- **THEN** three are transcribed at once, and the fourth starts as soon as one of them finishes

#### Scenario: Local provider
- **WHEN** the provider is local Whisper
- **THEN** the task runs one transcription at a time

#### Scenario: Deploy during several transcriptions
- **WHEN** a task is stopped while it runs three transcriptions
- **THEN** all three jobs are pending again at once

### Requirement: Local fallback runs one job at a time
When remote jobs fall back to local Whisper, Whisper SHALL transcribe one job at a time in the process. While Whisper is running or has a job waiting, the worker SHALL claim no job beyond those it is running.

#### Scenario: Remote provider outage
- **WHEN** a running job falls back to local Whisper
- **THEN** further pending jobs stay in the queue until Whisper is free

### Requirement: A remote job is checked often at first
The Amazon Transcribe provider SHALL check a running job every second for its first 30 seconds and every 3 seconds after that.

#### Scenario: Short recording
- **WHEN** a batch job completes 10 seconds after it starts
- **THEN** the transcript is read within about a second of completion
