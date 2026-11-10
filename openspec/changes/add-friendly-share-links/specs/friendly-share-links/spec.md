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
Every video watch link SendRec generates SHALL be `{BASE_URL}/{shareToken}` when the short path serves the token's shape, and `{BASE_URL}/watch/{shareToken}` for a token of any other shape.

#### Scenario: A token the short path does not serve
- **WHEN** SendRec generates a link for a video whose share token is neither 16 lowercase letters and digits nor 22 base64url characters
- **THEN** the link is `{BASE_URL}/watch/{shareToken}`, which opens the same page

#### Scenario: MPD metadata
- **WHEN** mpd-api reads a published recording's metadata
- **THEN** `watchUrl` is `{BASE_URL}/{shareToken}`

#### Scenario: App and email links
- **WHEN** the app shows or copies a share link, or SendRec sends a notification, comment, digest or retention email
- **THEN** the video's link is `{BASE_URL}/{shareToken}`

### Requirement: Configurable public hostname
The stack SHALL serve SendRec on a custom hostname when the environment's committed config names the hostname and its `us-east-1` ACM certificate, and SHALL keep the CloudFront hostname otherwise. No deploy context SHALL add or remove it.

#### Scenario: Hostname configured
- **WHEN** the environment config names a hostname and certificate
- **THEN** CloudFront serves that hostname with that certificate, `BASE_URL` is `https://{hostname}`, and the recordings bucket allows both that origin and the CloudFront origin

#### Scenario: Hostname absent
- **WHEN** the environment config names no hostname
- **THEN** the distribution has no alias and `BASE_URL` is its CloudFront hostname, whatever contexts the deploy passes

#### Scenario: Invalid hostname settings
- **WHEN** the config names an invalid hostname, or a certificate outside `us-east-1` or the stack's account
- **THEN** synthesis fails before any deploy
