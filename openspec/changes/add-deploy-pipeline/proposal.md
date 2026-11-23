## Why

SendRec has no release pipeline. Each release is built on an operator's machine, copied into production with `crane`, and applied as a hand-edited change set (`docs/agents/claude/production-release.md`). The `mpd-build-candidate` and `mpd-promote` workflows have never run: the repository has no roles, variables or `production` environment for them. mpd-api, the staff app and Cortex already deploy through a CDK pipeline in the deployment account, and mpd-api and the staff app release to production once a night. Sami chose option B on MPD-592 (agreed scope, 23 November 2026): put SendRec on the same AWS pipeline, run its migrations before each release, then add the same nightly release. This replaces MPD-513.

## What Changes

- A new CDK stack, `SendRecPipeline`, for the deployment account: a self-mutating CDK pipeline named `SendRec`, with `main` of this repository as its source through the deployment account's existing GitHub connection.
- The `Build` step builds the image inside AWS with `VERSION` set to the commit's first 8 characters and the `org.opencontainers.image.revision` label, and pushes it to staging's `sendrec` repository under the full commit SHA. A commit whose image is already there is not built again.
- Staging deploys `SendRecStaging` with that digest. Production waits at an `ApproveProduction` gate, then copies the same digest into production's `sendrec` repository and deploys `SendRecProduction` with it. Production never rebuilds.
- Each stack deploy runs the migration task definition, with the release image, before the service changes, through a custom resource. A failed migration fails the deploy before the service changes. The app keeps `MIGRATIONS_MODE=skip`.
- After each deploy, the pipeline checks that `/api/health` reports the release's version.
- Every deploy input that would identify an environment (production's account, network, bucket, certificate, task count, switches and MPD integration settings) comes from SSM parameters in the deployment account, read when the pipeline synthesizes. Nothing identifying is committed. Production synthesis still requires `productionTaskCount`.
- The nightly production release, ported from the staff app's (#604, #618) and mpd-api's (#609, #620): an EventBridge Scheduler schedule at **17:20 Australia/Sydney**, ten minutes after the staff app, starts a state machine that approves the newest staging-verified run, rejects older runs waiting at the gate, and releases only while the SSM parameter `/sendrec/release/hold` holds exactly `off`. Pipeline failures, failed release jobs and held releases email MPD API's alarm lists.
- The two GitHub workflows `mpd-build-candidate.yml` and `mpd-promote.yml` are removed. Production deploys stay inside AWS, not in a public repository's workflows.

## Impact

- New: `PipelineStack.java`, `PipelineConfig.java`, `NightlyRelease.java`, `MigrationRun.java` and their tests; `infrastructure/migration-runner.py`, `infrastructure/pipeline/*.sh` and their tests.
- Changed: `SendRecApp.java` (a `pipeline` target), `SendRecStack.java` (the migration run before the service), `build.gradle` (test dependencies), `infrastructure/README.md`, `infrastructure/DEPLOYMENT.md`, `docs/agents/claude/production-release.md`.
- Removed: `.github/workflows/mpd-build-candidate.yml`, `.github/workflows/mpd-promote.yml`.
- No application code or database migration. Nothing is deployed by this change. The first deploy of `SendRecPipeline` needs deploy rights in the deployment account and is a one-time step for MPD HQ's maintenance window, written in the README.

## Approval and delivery

MPD HQ assigned this on 23 November 2026 with Sami's approval (MPD-592, option B). The pull request stays a draft until the Review desk passes it and Sami says go.
