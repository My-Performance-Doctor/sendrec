## ADDED Requirements

### Requirement: mpd-api sets the patient summary
The integration API SHALL let mpd-api set or clear a managed recording's patient summary for its current media version, under the same credential, work fence and idempotency rules as the other integration mutations.

#### Scenario: Setting a summary
- **WHEN** mpd-api sends `PUT /api/integrations/mpd/videos/{videoId}/patient-summary` with the current media version, a summary of at most 1,200 characters and up to eight points, each with a start time and at most 300 characters
- **THEN** the recording stores that summary for that media version and the response says a summary is set

#### Scenario: Clearing a summary
- **WHEN** mpd-api sends the same request with a null summary and no points
- **THEN** the recording has no patient summary

#### Scenario: Invalid content
- **WHEN** the summary is blank, a point is blank or too long, a start time is negative or not a number, or there are more than eight points
- **THEN** the response is 400 and nothing changes

#### Scenario: Stale media
- **WHEN** the request names a media version other than the recording's current one
- **THEN** the response is 409 and nothing changes

### Requirement: Managed watch pages show the approved summary
A managed recording's watch page SHALL show its patient summary only when one is stored for the recording's current media version, and SHALL NOT show SendRec's own summary or chapters.

#### Scenario: A summary for the current media
- **WHEN** a viewer unlocks a managed recording with a stored summary for its current media version and a ready transcript
- **THEN** the page shows a Summary tab with the text and an "In this video" list of the points, each a button that plays the video from its start time, and a Transcript tab

#### Scenario: New media
- **WHEN** the recording's media version has advanced since the summary was stored
- **THEN** the page shows the transcript only

#### Scenario: SendRec's own summary
- **WHEN** a managed recording carries a summary or chapters from SendRec's AI
- **THEN** the page does not show them

### Requirement: Managed pages use TT Neoris headings when available
Managed pages SHALL set their headings in TT Neoris when the runtime image carries the font, and in Geist otherwise. This repository SHALL NOT contain the font.

#### Scenario: The font is in the image
- **WHEN** the configured brand font directory holds `tt-neoris-demibold.ttf`
- **THEN** `/fonts/brand/tt-neoris-demibold.ttf` serves it and managed page headings name TT Neoris first

#### Scenario: The font is absent
- **WHEN** the directory is unset or lacks the file
- **THEN** the font path answers 404 and headings render in Geist

### Requirement: The managed watch page offers its source
A managed recording's watch page SHALL end with a link to this repository's source and SHALL NOT credit or link to SendRec.

#### Scenario: A patient reaches the footer
- **WHEN** a viewer unlocks a managed recording
- **THEN** the footer shows a "Source code" link to the fork's repository and no "Recorded with SendRec" text

#### Scenario: An unmanaged share
- **WHEN** a viewer opens an unmanaged share
- **THEN** its footer is unchanged
