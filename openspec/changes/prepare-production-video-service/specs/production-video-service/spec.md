## Purpose

Make the existing SendRec service reproducibly deployable and recoverable with evidence before an approved production pilot.

## ADDED Requirements

### Requirement: Isolated reproducible environments

Production infrastructure SHALL use explicitly configured account, region, networking, identity and secret references with private encrypted media and database storage. Staging and production identities or data SHALL not mix.

#### Scenario: Production synthesis
- **WHEN** the approved production configuration is synthesized
- **THEN** the diff contains the intended isolated resources and no staging data migration

### Requirement: Staged deployment and rollback

A release SHALL identify its immutable image, migration and infrastructure revisions, pass staging checks and require explicit human production approval. Rollback SHALL preserve recordings, identities and pending integration events.

#### Scenario: Bad release
- **WHEN** a new application revision fails readiness
- **THEN** the prior verified image can be restored without deleting media or event state

### Requirement: Concurrent workers and recovery

Production SHALL support the proposed multi-task deployment without duplicate processing or lost accepted events, and SHALL verify automatic recovery after interrupted work.

#### Scenario: Two workers race
- **WHEN** multiple tasks process the same pending job or event
- **THEN** one logical result is retained and retries remain safe

#### Scenario: Processing task stops
- **WHEN** a task stops during transcription
- **THEN** automatic recovery completes or surfaces a visible failure within the documented recovery bound

### Requirement: Measured backup restoration

Production SHALL support the proposed RPO of 15 minutes and RTO of four hours, backed by measured isolated restoration of database, media versions and pending work. Source media SHALL not be automatically deleted without an approved retention policy.

#### Scenario: Restore drill
- **WHEN** synthetic state is restored into an isolated environment
- **THEN** videos decode, protection and captions work, pending events recover and measured recovery meets the approved objectives

### Requirement: Browser and API evidence

The release SHALL record actual Chrome, Safari and Firefox results for screen/camera recording, MP4/WebM upload, protected watch/embed playback, captions and interrupted-network recovery. A synthetic external client SHALL exercise the future Staff OS contract.

#### Scenario: Host browser blocked
- **WHEN** the execution host cannot run a browser safely
- **THEN** the browser check remains unverified until a supported host or documented manual check supplies evidence

#### Scenario: Future client
- **WHEN** the synthetic client authenticates and uses the recording API
- **THEN** it works without implementing Staff OS screens

### Requirement: Operational alerts and private evidence

The service SHALL alert on failed processing, delivery backlog, dead letters, unhealthy service and backup failure through a tested operational destination. Logs and committed evidence SHALL exclude secrets, signed URLs and patient identifiers.

#### Scenario: Synthetic fault
- **WHEN** a controlled delivery failure is introduced
- **THEN** the configured operator destination receives the corresponding alert without private payload data

### Requirement: Honest readiness and bounded rollout

Production readiness SHALL require the four integration changes to pass their contracts, the existing staging change to be closed with truthful evidence and a reviewed deployment diff. A proposal or passing synthesis SHALL not count as a deployed production service.

#### Scenario: Unfinished verification
- **WHEN** a required browser, identity, restore or integration check has not run
- **THEN** readiness remains incomplete and production activation does not proceed

### Requirement: Retention cannot bypass the approved policy

Managed source retention SHALL be disabled in application workers and personal/workspace configuration as well as storage lifecycle rules until the operator approves a policy. Stale retention settings or enabling email SHALL not authorize deletion.

#### Scenario: Old retention warning
- **WHEN** a managed video has an old retention warning and nonzero user or workspace retention_days, and retention plus cleanup workers run
- **THEN** the source stays available and staff cannot enable automatic deletion through settings

### Requirement: All enabled background work supports multiple tasks

Readiness SHALL inventory every startup worker, not only media processing and outbox delivery. Each enabled worker SHALL use a cross-task claim or leader lease; unsupported optional workers SHALL remain explicitly disabled.

#### Scenario: Overlapping schedules
- **WHEN** two tasks run media recovery, cleanup, retention, digest/onboarding or legacy-webhook schedules concurrently and a leader stops
- **THEN** controlled sinks observe one logical effect and pending work recovers without duplicate sends
