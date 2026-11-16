## ADDED Requirements

### Requirement: Transcription deadline follows the recording
A transcription job SHALL have a deadline of 30 minutes plus four times the recording's duration. The added time SHALL be capped at 4 hours. A recording with no known duration SHALL get the 30-minute base. The stuck-job reset SHALL requeue a job only one minute after that job's own deadline has passed.

#### Scenario: Ten-minute recording
- **WHEN** a 10-minute recording is transcribed
- **THEN** the job may run for up to 70 minutes before it fails

#### Scenario: Long recording still inside its deadline
- **WHEN** a 10-minute recording has been transcribing for 45 minutes
- **THEN** the stuck-job reset leaves it running

#### Scenario: Unprobed recording whose worker died
- **WHEN** a recording with no known duration has been in `processing` for 45 minutes
- **THEN** the stuck-job reset requeues it

### Requirement: Encodes take the CPU before local transcription
Local Whisper SHALL run at the lowest CPU priority when `nice` is available. A transcription worker SHALL NOT claim a job while every encoder slot on its task is in use.

#### Scenario: Upload while a transcript is running
- **WHEN** an encode starts on a task that is running Whisper
- **THEN** the encode gets the CPU first, and Whisper continues with the CPU that is left

#### Scenario: Pending transcript while a task encodes
- **WHEN** a transcription is pending and this task's encoder slots are all in use
- **THEN** this task's worker leaves the job pending for an idle task, or for itself after the encode ends
