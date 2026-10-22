## Why

The runtime image was built `FROM alexneamtu/sendrec-base:latest`, a personal Docker Hub image MPD neither builds nor pins. In October 2026 Alpine's ca-certificates 20261020 removed three eMudhra roots after the 2026 eMudhra intermediate compromise, and Go 1.26.10 shipped the matching fix. Staging needed both, from a base MPD controls.

## What Changes

- Build `Dockerfile.base` in MPD's staging account, push it to the ECR repository `sendrec-base` (immutable tags, scan on push) and pin the `Dockerfile`'s `BASE_IMAGE` argument to the pushed digest.
- Fail any image build whose CA store trusts `emSign ECC Root CA - G3`, `emSign Root CA - C1`, `emSign ECC Root CA - C3` or `AAA Certificate Services`. `scripts/check-ca-roots.sh` runs in `Dockerfile.base`, `Dockerfile` and `Dockerfile.standalone`, so the store that ships is the one checked.
- Build whisper.cpp with `GGML_NATIVE=OFF`, so the base does not depend on the build host's CPU.
- Build on Go 1.26.10.
- Let CI, local stacks and CDK deploys build without reading ECR, or read it with the right role: CI and `make docker-up` build `Dockerfile.base` locally, and a repository policy lets the CDK image-publishing role pull the base.

## Capabilities

### New Capabilities

- `runtime-base-image`: MPD builds, pins and checks the image SendRec runs on.

### Modified Capabilities

None.

## Impact

`Dockerfile`, `Dockerfile.base`, `Dockerfile.standalone`, `scripts/check-ca-roots.sh`, CI, the Makefile, compose files and the infrastructure runbook. One ECR repository and its policy in staging account `537421187871`. Codifying the repository in CDK and a GitHub build role with ECR read are follow-ups.

## Approval and delivery

Recorded after implementation, from PR #8 and its review on 2026-10-22, because no change covered this work. Sami reviews this record.
