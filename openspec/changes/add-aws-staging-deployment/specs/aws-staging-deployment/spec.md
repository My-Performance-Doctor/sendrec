## Purpose

Provide a reproducible Australian staging environment where MPD staff can evaluate SendRec recording and sharing using synthetic data before integrating it with patient systems.

## ADDED Requirements

### Requirement: Dedicated staging resources
The deployment SHALL isolate SendRec storage, database state and application compute from existing MPD applications in staging account `537421187871`, region `ap-southeast-2`.

#### Scenario: Deployment scope
- **WHEN** the deployment is prepared and applied
- **THEN** its resource changes target only dedicated SendRec resources and references to the existing staging network
- **AND** no production account or existing patient application is modified

### Requirement: Secure application origin
The application SHALL accept browser login and recording traffic only through HTTPS with a valid certificate for its configured hostname.

#### Scenario: Certificate pending
- **WHEN** deployment is attempted while the certificate is not issued
- **THEN** the deployment preflight rejects the attempt before creating the stack
- **AND** no application or maintenance endpoint is published

#### Scenario: HTTPS ready
- **WHEN** DNS and the certificate are ready
- **THEN** the configured hostname serves the application with a trusted certificate
- **AND** HTTP requests redirect to that HTTPS hostname

### Requirement: Private recordings and role credentials
Recordings SHALL remain private and accessible only through authorized application access or time-limited signed requests. AWS deployment SHALL use role credentials without permanent S3 access keys. Explicit credentials SHALL remain supported for local development.

#### Scenario: Object access controls
- **WHEN** a synthetic recording is uploaded
- **THEN** authorized signed upload and playback requests succeed
- **AND** an anonymous unsigned request for the same object fails

#### Scenario: Temporary credentials
- **WHEN** the service uses temporary role credentials without explicit S3 keys
- **THEN** generated signed requests include the session credential information required for successful upload and download

#### Scenario: Local compatibility
- **WHEN** local development supplies a complete explicit S3 credential pair
- **THEN** upload and download continue using those credentials

### Requirement: Restricted account bootstrap
Public registration SHALL be disabled by default. Initial owner registration SHALL be available only with explicit restricted network access, and SHALL be disabled before wider access opens.

#### Scenario: Default access
- **WHEN** an anonymous visitor attempts registration after normal deployment
- **THEN** registration fails without a valid invitation

#### Scenario: Owner setup
- **WHEN** bootstrap registration is enabled
- **THEN** access is limited to an explicit operator network range
- **AND** the owner can register from that range
- **AND** requests outside that range cannot reach registration

### Requirement: Local processing and verified feature checks
Transcription SHALL process synthetic audio within the staging account. Deployment completion SHALL require successful recording or upload, playback, password protection, captions and signed webhook checks.

#### Scenario: Synthetic video evaluation
- **WHEN** a synthetic speech recording is submitted
- **THEN** it becomes playable and yields a nonempty VTT transcript
- **AND** a password-protected link rejects the wrong password and plays with the correct password
- **AND** a controlled receiver verifies webhook signatures and rejects a tampered signature

#### Scenario: Processing failure
- **WHEN** model initialization or transcription fails
- **THEN** the failure remains visible in evaluation results
- **AND** the deployment is not reported as fully verified

### Requirement: Recoverable state and truthful handoff
The deployment SHALL preserve recordings and database state across task replacement and SHALL document retained resources, verified outcomes and unresolved operator actions.

#### Scenario: Task replacement
- **WHEN** the service task is replaced after a synthetic recording succeeds
- **THEN** that recording and its database entry remain accessible

#### Scenario: Incomplete deployment
- **WHEN** DNS, bootstrap or feature verification is unfinished
- **THEN** the handoff names the pending action and reports deployment as incomplete
- **AND** it provides the exact repository revision and resumable next step
