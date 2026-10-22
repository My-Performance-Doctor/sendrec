## ADDED Requirements

### Requirement: Staging expires old media versions
The staging recordings bucket SHALL expire noncurrent object versions 30 days after they become noncurrent and abort incomplete multipart uploads 7 days after they start.

#### Scenario: Deleted staging media
- **WHEN** a staging recording object is deleted or replaced on a versioned bucket
- **THEN** its old version is removed 30 days later

### Requirement: Production keeps media versions until a retention policy
The production recordings bucket SHALL abort incomplete multipart uploads after 7 days and SHALL NOT expire any object version until MPD approves a clinical retention policy. Any later noncurrent expiry SHALL be at least the RDS point-in-time recovery window.

#### Scenario: Deleted production media
- **WHEN** a production recording object is deleted or replaced
- **THEN** its old version stays until an operator restores or removes it

#### Scenario: Abandoned upload
- **WHEN** a multipart upload to either bucket is not completed within 7 days
- **THEN** its parts are removed
