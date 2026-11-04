## ADDED Requirements

### Requirement: Managed links ask for a video code
A managed recording's password page SHALL ask for a video code in MPD's look, and SHALL NOT show the recording's details before the viewer passes it.

#### Scenario: A patient opens the link
- **WHEN** a viewer without a valid watch cookie opens `/watch/{token}` for a managed recording
- **THEN** the page asks for a video code and shows neither the recording's title nor its player

#### Scenario: An unmanaged share
- **WHEN** a viewer opens the link of a password-protected share that is not managed
- **THEN** SendRec's password page and its free-form password apply

### Requirement: Code entry forgives how it was entered
The page SHALL send a code in its issued form whatever its case, spacing or hyphens, and SHALL find a code in a pasted message. Any other value SHALL be sent as entered, apart from surrounding spaces.

#### Scenario: A code typed in lowercase without hyphens
- **WHEN** a viewer types `k7qm2xpa9rtfhd4w`
- **THEN** the field shows `K7QM-2XPA-9RTF-HD4W` and the page submits it

#### Scenario: A whole message pasted
- **WHEN** a viewer pastes a message that contains a code
- **THEN** the field holds only the code and the page submits it

#### Scenario: An older password
- **WHEN** a viewer enters a value that cannot be a complete code
- **THEN** it stays as entered, and only surrounding spaces are removed when it is submitted

### Requirement: Refusals say what happened
The page SHALL distinguish a wrong code, a video locked after too many tries and a failed request.

#### Scenario: Locked after too many tries
- **WHEN** verification answers 429, even for the right code
- **THEN** the page says there have been too many tries and disables entry

### Requirement: Unavailable links show one page
An unpublished, deleted or expired managed link, or an unknown link, SHALL show one MPD page when opened, which SHALL NOT say which of these it is or link to SendRec. Other paths on the same token SHALL keep their existing answers.

#### Scenario: An unpublished managed link
- **WHEN** a viewer opens `/watch/{token}` for an unpublished managed recording
- **THEN** the response is 404 with the MPD page

#### Scenario: The API for the same recording
- **WHEN** a client requests `/api/watch/{token}` for an unpublished managed recording
- **THEN** the response is the existing 404 JSON

### Requirement: Managed watch pages use MPD's look
A managed recording's watch page SHALL use MPD's light look and SHALL NOT link its logo to SendRec's sign-in.

#### Scenario: A patient unlocks the video
- **WHEN** a viewer with a valid watch cookie opens the link
- **THEN** the video plays on MPD's page and the logo is not a link
