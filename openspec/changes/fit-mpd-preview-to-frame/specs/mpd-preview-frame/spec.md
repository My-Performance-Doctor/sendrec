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
- **THEN** the status text shows on its own, without an empty player

#### Scenario: Styles under the page's CSP
- **WHEN** the page is served
- **THEN** its styles carry the request's CSP nonce and it has no inline style attribute
