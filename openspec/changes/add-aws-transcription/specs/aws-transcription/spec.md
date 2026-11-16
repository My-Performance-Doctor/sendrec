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
- **THEN** the transcript is marked failed and the audio, output and job are deleted

### Requirement: Language selection
The provider SHALL use `TRANSCRIPTION_LANGUAGE_CODE` (default `en-AU`) for `auto` and `en`, a full locale as given, and Transcribe's language identification for any other language.

#### Scenario: Default MPD recording
- **WHEN** a recording's language is `auto`
- **THEN** the job runs with `en-AU`

### Requirement: Temporary transcription objects expire
The recordings bucket SHALL expire current and noncurrent objects under `transcribe/` after one day.

#### Scenario: Task stopped mid-job
- **WHEN** a task stops after uploading audio and before deleting it
- **THEN** the audio, and any version of it, is gone within two days

### Requirement: Off-task transcription does not wait for encodes
A worker using a provider other than local Whisper SHALL claim a transcription while its task encodes.

#### Scenario: Upload while encoding
- **WHEN** a transcription is pending and the task is encoding
- **THEN** a worker using the `aws` provider claims it at its next tick
