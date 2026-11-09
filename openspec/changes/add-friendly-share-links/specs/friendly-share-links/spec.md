## ADDED Requirements

### Requirement: Short watch path
SendRec SHALL serve a video's watch page at `/{shareToken}` with the same access rules as `/watch/{shareToken}`, and SHALL keep serving `/watch/{shareToken}`.

#### Scenario: Short link opens the video
- **WHEN** a viewer opens `/{shareToken}` for a published video
- **THEN** the watch page renders as it does at `/watch/{shareToken}`

#### Scenario: Protected short link
- **WHEN** a viewer opens `/{shareToken}` for an MPD recording with a password
- **THEN** the password form renders, and after the correct password the recording plays and records playback

#### Scenario: Links already sent
- **WHEN** a viewer opens `/watch/{shareToken}`, on the custom hostname or the CloudFront hostname
- **THEN** the watch page renders as before

#### Scenario: App paths are not tokens
- **WHEN** a browser opens `/login`, `/library` or another app path
- **THEN** the app renders, not a watch page

### Requirement: Friendly share tokens
SendRec SHALL give each new video a share token of 16 characters drawn uniformly from lowercase letters and digits, and SHALL leave existing tokens unchanged.

#### Scenario: New video
- **WHEN** a video is created or uploaded
- **THEN** its share token matches `[a-z0-9]{16}`

### Requirement: Generated links use the short form
Every video watch link SendRec generates SHALL be `{BASE_URL}/{shareToken}`.

#### Scenario: MPD metadata
- **WHEN** mpd-api reads a published recording's metadata
- **THEN** `watchUrl` is `{BASE_URL}/{shareToken}`

#### Scenario: App and email links
- **WHEN** the app shows or copies a share link, or SendRec sends a notification, comment, digest or retention email
- **THEN** the video's link is `{BASE_URL}/{shareToken}`

### Requirement: Configurable public hostname
The stack SHALL serve SendRec on a custom hostname when a deploy supplies the hostname and its `us-east-1` ACM certificate, and SHALL keep the CloudFront hostname otherwise.

#### Scenario: Hostname supplied
- **WHEN** a deploy supplies `domainName` and `domainCertificateArn`
- **THEN** CloudFront serves that hostname with that certificate, `BASE_URL` is `https://{domainName}`, and the recordings bucket allows both that origin and the CloudFront origin

#### Scenario: Hostname absent
- **WHEN** a deploy supplies neither context
- **THEN** the distribution has no alias and `BASE_URL` is its CloudFront hostname

#### Scenario: Invalid hostname settings
- **WHEN** a deploy supplies only one context, an invalid hostname, or a certificate outside `us-east-1` or the stack's account
- **THEN** synthesis fails before any deploy
