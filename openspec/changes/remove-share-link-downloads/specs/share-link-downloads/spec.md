## ADDED Requirements

### Requirement: Shared recordings offer no download
A recording opened through a share link SHALL NOT offer a download, and no recording SHALL be able to turn share-link downloads on.

#### Scenario: Watch page
- **WHEN** a viewer opens a recording's watch page
- **THEN** it shows no Download button, no `og:video` tags and no JSON-LD `contentUrl`, and the video carries `controlsList="nodownload"`

#### Scenario: Share-link download endpoint
- **WHEN** anyone requests `/api/watch/{shareToken}/download`
- **THEN** the request is refused with 403 and no file link

#### Scenario: Browser context menu
- **WHEN** a viewer right-clicks the video on a watch, embed or playlist page
- **THEN** the browser's video menu, with its "Save video as" item, does not open

#### Scenario: Existing and new recordings
- **WHEN** the migration has run, or a recording is created afterwards
- **THEN** the recording's downloads are off, and the database rejects turning them on

#### Scenario: Staff sharing settings
- **WHEN** staff open a recording's sharing settings
- **THEN** there is no Downloads toggle, and `PUT /api/videos/{id}/download-enabled` does not exist

### Requirement: Staff keep their own download
Signed-in staff SHALL still be able to download a recording they can access from the library and detail page.

#### Scenario: Library or detail download
- **WHEN** signed-in staff choose Download on a recording in the library or detail page
- **THEN** they receive a short-lived download link, as before
