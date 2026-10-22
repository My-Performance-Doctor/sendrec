## ADDED Requirements

### Requirement: MPD-built pinned runtime base
The SendRec image SHALL build on a base image MPD builds from `Dockerfile.base`, stored in MPD's own registry and pinned by digest.

#### Scenario: Release build
- **WHEN** the `Dockerfile` builds without a `BASE_IMAGE` override
- **THEN** its runtime stage uses the pinned `sendrec-base` digest in staging account `537421187871`

#### Scenario: Build without registry access
- **WHEN** CI or a local stack builds the image without ECR credentials
- **THEN** it builds `Dockerfile.base` locally and passes that image as `BASE_IMAGE`

### Requirement: Checked CA store
Every SendRec image build SHALL fail if the image's CA store trusts a root removed after the 2026 eMudhra intermediate compromise.

#### Scenario: Blocked root present
- **WHEN** any file under `/etc/ssl/certs` holds `emSign ECC Root CA - G3`, `emSign Root CA - C1`, `emSign ECC Root CA - C3` or `AAA Certificate Services`
- **THEN** the build fails and names the root

#### Scenario: Truncated store
- **WHEN** the store holds fewer than 100 distinct roots or a certificate that is not valid base64 or not a whole DER certificate
- **THEN** the build fails
