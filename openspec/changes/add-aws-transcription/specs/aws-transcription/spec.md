## ADDED Requirements

### Requirement: Amazon Transcribe provider
With `TRANSCRIPTION_PROVIDER=aws`, SendRec SHALL transcribe a recording's extracted audio with an Amazon Transcribe batch job in the stack's region. It SHALL write the audio and the job's output only under `transcribe/` in the recordings bucket, and SHALL delete both and the job once the transcript is read or the job fails.

#### Scenario: Recording transcribed
- **WHEN** a recording with speech is transcribed
- **THEN** its transcript is stored as caption segments with start and end times, and no audio, output or job remains

#### Scenario: Silent recording
- **WHEN** Transcribe returns no words
- **THEN** the transcript status is `no_audio`

#### Scenario: Failed job
- **WHEN** the Transcribe job fails
- **THEN** local Whisper transcribes the same audio, and the audio, output and job are deleted

#### Scenario: Job abandoned while running
- **WHEN** a job is still running at the transcript's deadline or the task's shutdown
- **THEN** the audio is deleted at once, and the output is deleted when the job writes it within the cleanup's wait

### Requirement: Transient Transcribe errors do not fail a transcript
The provider SHALL retry a failed job status check until the job's deadline and SHALL try a job start three times. When Transcribe cannot finish the job, SendRec SHALL transcribe the audio with local Whisper within the job's deadline, and SHALL mark the transcript failed only when Whisper fails too.

#### Scenario: Throttled status checks
- **WHEN** status checks of a running job fail and later succeed
- **THEN** the transcript is published from that job

#### Scenario: Transcribe unavailable
- **WHEN** every start of a job fails
- **THEN** the transcript is published from local Whisper

### Requirement: Transcription stops cleanly at shutdown
At shutdown SendRec SHALL hand the running transcription back to the queue, then cancel it so its provider deletes the staged audio and job before the task stops.

#### Scenario: Deploy during a Transcribe job
- **WHEN** a task stops while its Transcribe job runs
- **THEN** the job is pending again for another task and the staged audio is deleted

### Requirement: Transcribe's bucket access is limited to transcribe/
The task role SHALL start Transcribe jobs only with output under `transcribe/` in the recordings bucket, and SHALL deny object reads and writes an AWS service makes for it outside `transcribe/`.

#### Scenario: Job output elsewhere
- **WHEN** a job is started with output outside `transcribe/`
- **THEN** the start is denied

### Requirement: Language selection
The provider SHALL use `TRANSCRIPTION_LANGUAGE_CODE` (default `en-AU`) for `auto` and `en`, a full locale as given, the usual locale of a common two-letter language, and Transcribe's language identification for any other language.

#### Scenario: Default MPD recording
- **WHEN** a recording's language is `auto`
- **THEN** the job runs with `en-AU`

### Requirement: Temporary transcription objects expire
The recordings bucket SHALL expire current and noncurrent objects under `transcribe/` after one day and SHALL remove expired delete markers there.

#### Scenario: Task stopped mid-job
- **WHEN** a task stops after uploading audio and before deleting it
- **THEN** the audio, and any version of it, is gone within two days

### Requirement: Off-task transcription does not wait for encodes
A worker using a provider other than local Whisper SHALL claim a transcription while its task encodes.

#### Scenario: Upload while encoding
- **WHEN** a transcription is pending and the task is encoding
- **THEN** a worker using the `aws` provider claims it at its next tick
