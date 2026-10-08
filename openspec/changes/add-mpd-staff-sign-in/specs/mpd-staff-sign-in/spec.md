## Purpose

Allow authorized MPD staff to use the existing SendRec recorder directly and through a future client with consistent identity and access.

## ADDED Requirements

### Requirement: MPD login and future client entry

The system SHALL support native MPD OIDC login and POST /api/auth/mpd/session with an approved staff access token. Both entry points SHALL use the same MPD access contract and managed recording session.

#### Scenario: Two entry points
- **WHEN** an authorized synthetic staff identity uses native sign-in and the synthetic future-client exchange
- **THEN** both resolve to the same local recording account and permissions

#### Scenario: Cross-environment token
- **WHEN** a client presents the patient pool, an unapproved client or another environment's issuer
- **THEN** no recording session is issued

### Requirement: Safe callback and account binding

Native login SHALL bind state, nonce and PKCE to a short-lived login transaction and SHALL NOT place bearer tokens in redirect URLs. Local identities SHALL be uniquely bound to issuer and subject; email equality alone SHALL NOT link an existing account.

#### Scenario: First login
- **WHEN** an invited active staff member has an MPD video grant and no local identity
- **THEN** one local account and managed membership are created atomically

#### Scenario: Existing email collision
- **WHEN** a new external identity has an existing local account's email
- **THEN** login requires explicit audited linking and cannot take over that account

#### Scenario: Callback replay
- **WHEN** a callback or one-use handoff code is reused or has mismatched state
- **THEN** the exchange fails without issuing a session

### Requirement: Managed authorization on every route

Managed recording and workspace routes SHALL enforce MPD-derived capabilities regardless of local role, personal context, API key or refresh path. Unapproved local-password and social-login paths SHALL NOT bypass MPD authorization for a managed staff identity.

#### Scenario: Read-only staff
- **WHEN** a staff member with read but no record/manageOwn calls a recording mutation
- **THEN** the mutation is forbidden while permitted reads work

#### Scenario: Header escalation
- **WHEN** a caller changes workspace headers or presents an old API key
- **THEN** the caller cannot gain access beyond the current MPD grant

### Requirement: Session expiry and offboarding

Managed sessions SHALL not outlive the valid Cognito authorization and SHALL recheck MPD rights by validUntil. Membership or permission loss SHALL stop new managed requests within 60 seconds, including existing sessions. Unavailable revalidation after expiry SHALL fail closed.

#### Scenario: Existing session disabled
- **WHEN** membership ends after sign-in
- **THEN** refresh and managed operations are refused within 60 seconds

#### Scenario: Future-client token expires
- **WHEN** the Cognito token used for session exchange expires
- **THEN** that exchange cannot sustain the SendRec session without a new valid staff token

### Requirement: Native media functionality remains usable

Authorized staff SHALL continue using existing screen/camera recording, direct S3 uploads, processing, watch/embed players, captions and retranscription. Media bytes SHALL not be proxied through mpd-api.

#### Scenario: Existing recorder
- **WHEN** an authorized synthetic staff member records and uploads a video
- **THEN** existing processing and protected playback work under the mapped account

### Requirement: Scoped integration service

The system SHALL provide a separate service credential for MPD metadata, transcript, password and publication operations on managed workspace videos. It SHALL NOT grant staff impersonation, recording, global browsing, workspace administration or deletion.

#### Scenario: Allowed adapter operation
- **WHEN** the authorized MPD service requests a managed video transcript
- **THEN** the bounded transcript response identifies the correct video and version

#### Scenario: Credential overreach
- **WHEN** the service attempts a staff login, deletion or access outside its bound workspace
- **THEN** the request is refused and records no user impersonation

### Requirement: Managed publication and protected links

A new managed recording SHALL remain unavailable through public share/watch/embed routes until an authorized sharing action publishes it. Password and publication state SHALL be enforced on watch, embed, transcript, download and derived media access. New signed media URLs SHALL expire within five minutes.

#### Scenario: Unpublished share token
- **WHEN** a caller knows a new managed recording's share token before publication
- **THEN** public playback and derived media access are refused

#### Scenario: Protected published video
- **WHEN** an authorized action publishes a password-protected recording
- **THEN** missing or incorrect passwords fail on watch and embed while the correct password succeeds

### Requirement: Patient mapping stays external

The system SHALL store the staff and tenant identity required for media ownership but SHALL NOT add patient identifiers or patient association metadata to recordings. Clinical association SHALL remain in mpd-api.

#### Scenario: Recording payload
- **WHEN** a client tries to set a patient association on a recording
- **THEN** SendRec does not persist that association or infer one from a thumbnail

### Requirement: Playback survives URL expiry

Watch and embed SHALL renew expiring signed URLs after repeating their access checks, preserve playback position and paused state, and stop if renewal is refused.

#### Scenario: Long protected playback
- **WHEN** a recipient plays a synthetic recording longer than 15 minutes or pauses and seeks across URL expiry
- **THEN** authorized renewal allows playback to continue without a stale-signature failure

#### Scenario: Access withdrawn before renewal
- **WHEN** publication or the required access grant is withdrawn before URL renewal
- **THEN** renewal fails and no new media URL is issued

### Requirement: Managed account lifecycle preserves media

Managed staff SHALL NOT delete their account or managed workspace through self-service paths or change automatic source-retention settings. Offboarding SHALL revoke access without deleting workspace media.

#### Scenario: Self-service lifecycle bypass
- **WHEN** managed staff call account deletion, workspace deletion or personal/workspace retention updates directly
- **THEN** the request is refused and existing media remains unchanged
