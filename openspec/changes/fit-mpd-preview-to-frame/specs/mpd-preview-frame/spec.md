## ADDED Requirements

### Requirement: The staff preview fits the frame that shows it
The `/mpd-preview` page SHALL fit its player to the window that shows it, keeping the recording's aspect ratio, using only styles its Content Security Policy allows.

#### Scenario: Framed by Staff OS
- **WHEN** Staff OS frames the preview at 16:9 and the handoff is redeemed
- **THEN** the whole recording and its controls show inside the frame, letterboxed on black, with no heading or status above them

#### Scenario: Recording of another shape
- **WHEN** the recording is wider or taller than the frame
- **THEN** it is scaled to fit, not cropped

#### Scenario: Checking or lost access
- **WHEN** access is still being checked, or the preview has stopped because access ended
- **THEN** the status text shows on its own, without an empty player or any recording details

#### Scenario: Styles under the page's CSP
- **WHEN** the page is served
- **THEN** its styles carry the request's CSP nonce and it has no inline style attribute

### Requirement: A preview opened in its own tab names the recording
Opened outside a frame, once its media has loaded, the `/mpd-preview` page SHALL show a header above the player with the recording's title, its recording date and whether patients can open its link, as the patient watch gate would decide. The title SHALL appear nowhere else, including the tab title, and the header SHALL be cleared when access ends.

#### Scenario: Opened in its own tab
- **WHEN** staff open the preview in a tab of its own and its media loads
- **THEN** a header above the player shows the recording's title, its recording date and whether patients can open its link

#### Scenario: Link no longer open to patients
- **WHEN** the recording is unpublished or its link has expired
- **THEN** the header says patients can't open the link yet, or that the patient link has expired

#### Scenario: Framed by Staff OS
- **WHEN** Staff OS frames the preview
- **THEN** the header is only visually hidden, and the video fills the frame
