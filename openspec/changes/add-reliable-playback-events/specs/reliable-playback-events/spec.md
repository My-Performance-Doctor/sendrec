## Purpose

Deliver recoverable processing and playback facts from SendRec so MPD can update reports without treating page access as viewing.

## ADDED Requirements

### Requirement: First actual playback

The system SHALL emit video.playback_started only after a player starts and media time advances, once per playback session and media version. Page opens, preloads, seeks alone, buffering without progress and denied access SHALL NOT qualify.

#### Scenario: Actual play
- **WHEN** authorized playback starts and advances media time
- **THEN** one playback-start event is stored

#### Scenario: Page opened only
- **WHEN** watch or embed is rendered but playback does not advance
- **THEN** no playback-start event is stored

### Requirement: Honest viewer attribution

Playback events SHALL carry a server-derived viewerClass of staff_preview, recipient or anonymous. Only an independently verified recipient grant SHALL produce recipient. A client-provided identity or email SHALL NOT establish attribution.

#### Scenario: Staff preview
- **WHEN** an authenticated staff member previews a video
- **THEN** the event is classified staff_preview

#### Scenario: Anonymous playback
- **WHEN** a valid shared-link viewer plays without verified identity
- **THEN** the event is anonymous and does not identify a named patient

### Requirement: Versioned event contract

Managed events SHALL use schemaVersion 1, eventId, eventType, occurredAt, videoId, mediaVersion, ownerStaffId, tenantId and data. Types SHALL include video.ready, video.transcript_ready, video.playback_started and video.deleted. Transcript-ready data SHALL include transcriptVersion so later retranscription cannot be overwritten by an older completion. Events SHALL exclude patient metadata, transcript content, passwords, bearer tokens and signed media URLs.

#### Scenario: Caption ready later
- **WHEN** captions finish after media becomes playable
- **THEN** a distinct versioned transcript-ready event is produced

#### Scenario: Immutable replay
- **WHEN** a failed event is replayed
- **THEN** eventId and raw event body remain identical

### Requirement: Atomic durable acceptance

State changes and their managed events SHALL be durable together or neither SHALL be visible. Accepted playback facts SHALL survive process failure with their pending events.

#### Scenario: Crash after commit
- **WHEN** the application exits after a processing transition commits but before delivery
- **THEN** the replacement worker delivers the retained event

#### Scenario: Rolled-back transition
- **WHEN** the transaction that changes readiness rolls back
- **THEN** no ready event is delivered

### Requirement: Signed delivery and destination ownership

Managed integration delivery SHALL target a deployment-controlled MPD receiver independently of editable user notification settings. Each attempt SHALL sign the delivery timestamp plus raw body, carry a key identifier and refresh its timestamp without changing the event.

#### Scenario: Tampered attempt
- **WHEN** the body or delivery timestamp changes after signing
- **THEN** the receiver can reject the attempt

#### Scenario: User disables notifications
- **WHEN** a staff member disables personal notifications
- **THEN** managed MPD event delivery remains enabled

### Requirement: Recoverable retries and replay

Delivery SHALL use durable attempts and leases, retry transient errors for 24 hours and retain exhausted or blocked events for operator replay. Acknowledged evidence SHALL remain for at least 30 days; unacknowledged events SHALL not be automatically discarded.

#### Scenario: Receiver outage
- **WHEN** the receiver stays unavailable across task replacements
- **THEN** pending attempts survive and eventually deliver or enter a visible dead-letter state

#### Scenario: Receiver committed but reply lost
- **WHEN** the receiver commits then the response is lost
- **THEN** retry uses the same eventId so the receiver can deduplicate

### Requirement: Compatibility and event order

Legacy video.viewed and user webhooks SHALL retain their prior meanings. Managed delivery SHALL declare at-least-once unordered semantics; version and deletion information SHALL let consumers reject stale transitions.

#### Scenario: Legacy subscriber
- **WHEN** an existing subscriber receives video.viewed
- **THEN** its page-access meaning is unchanged

#### Scenario: Delayed old version
- **WHEN** an old playback event arrives after replacement or deletion
- **THEN** the consumer has sufficient identity/version data to avoid changing the current media state
