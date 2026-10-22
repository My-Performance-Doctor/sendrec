## ADDED Requirements

### Requirement: Gate pages hide the recording title
The watch and embed password and email-gate pages SHALL NOT include the recording's title before the viewer passes the gate.

#### Scenario: Password-protected share
- **WHEN** a viewer without a valid watch cookie opens `/watch/{token}` or `/embed/{token}` for a password-protected recording
- **THEN** the page's HTML title and body show a generic title and not the recording's title

#### Scenario: Email-gated share
- **WHEN** a viewer without a valid email-gate cookie opens the watch or embed page
- **THEN** the page's HTML title and body show a generic title and not the recording's title

### Requirement: Per-video password lockout
Share password verification SHALL refuse attempts on a video for 15 minutes once it has had 10 wrong passwords within a 15-minute window, whatever addresses they came from.

#### Scenario: Too many wrong passwords
- **WHEN** a video has had 10 wrong passwords since its current window started, less than 15 minutes ago
- **THEN** the next verification answers 429 without checking the password, including when the password is correct

#### Scenario: Window expired
- **WHEN** the window started 15 minutes ago or more
- **THEN** verification checks the password again, and a wrong password starts a new window with one failure

#### Scenario: Lockout state unavailable
- **WHEN** the lockout state cannot be read
- **THEN** verification is refused
