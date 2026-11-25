## ADDED Requirements

### Requirement: Operator import of a Vimeo video
SendRec SHALL create one managed, unpublished video in the MPD workspace for a Vimeo video whose file is already in the recordings bucket, when asked with an import token. The video SHALL keep the original recording date, SHALL be encoded by the usual normalize sweep, one import at a time, SHALL get no transcript, and SHALL carry no title or other text from Vimeo.

#### Scenario: A Vimeo video is imported
- **WHEN** the import is called with a Vimeo id, its recording date and duration, and the file is in place as mp4
- **THEN** one ready, unpublished managed video exists, dated on the recording date and waiting for the normalize sweep, its thumbnail is started, no transcript is queued, and mpd-api is told it is ready

#### Scenario: The file is not in place
- **WHEN** the file is missing or is not mp4
- **THEN** the import answers 409 and creates nothing

### Requirement: One video per Vimeo id
SendRec SHALL never create a second video for a Vimeo id it has already imported.

#### Scenario: A rerun
- **WHEN** the import is called again for the same Vimeo id
- **THEN** it answers 200 with the first video and makes no second one

#### Scenario: The imported video was deleted
- **WHEN** the import is called for a Vimeo id whose imported video was deleted
- **THEN** it answers 409 and creates nothing

### Requirement: Its own credentials
The import SHALL accept only its own service tokens and SHALL be off unless its tokens and owning user are configured.

#### Scenario: An MPD service token
- **WHEN** the import is called without an import token
- **THEN** it answers 401
