# Design

## Context

SendRec runs as two CloudFormation stacks, `SendRecStaging` and `SendRecProduction`, each in its own account. Both run an immutable image by digest from their own account's `sendrec` ECR repository (immutable tags, tagged with the full commit SHA), and both have a migration task definition. The app runs with `MIGRATIONS_MODE=skip`, so a release must run that definition before the service changes. Today an operator does all of this by hand from Leviathan.

mpd-api (`DeploymentPipelineStack.java`), the staff app (`pipeline_stack.py`, `nightly_release.py`) and Cortex each deploy through a self-mutating CDK pipeline in the deployment account. Staging and production accounts are bootstrapped to trust the deployment account, and the deployment account holds the GitHub connection those pipelines use. mpd-api and the staff app add a production gate and a nightly release that approves it.

This repository is public. It already names the staging account and staging network, but never production's account or anything that identifies production.

## Goals / Non-Goals

**Goals:**
- Every merge to `main` builds one image inside AWS and deploys it to staging. Production deploys that same digest after a gate, and the nightly release opens the gate.
- Migrations run before the service changes, on every release, in both environments.
- Nothing identifying production lands in the repository.
- Reuse the release logic already proven in the staff app and mpd-api.

**Non-Goals:**
- Deploying the pipeline. That needs deploy rights in the deployment account and happens in a maintenance window.
- Changing the application, its image, or how production alarms work.

## Decisions

### D1. A CDK Pipelines stack in the deployment account, like mpd-api's
`SendRecPipeline` defines a `CodePipeline` (V2, self-mutating, cross-account keys) named `SendRec`. Its source is `main` of `My-Performance-Doctor/sendrec` through the deployment account's GitHub connection. Its stages are `Source`, `Build`, `UpdatePipeline`, `Staging`, `ApproveProduction` and `Production`, as in the staff app, so the release state machine ports with only names changed. `SendRecApp` synthesizes it when `targetEnvironment=pipeline`. The existing `staging` and `production` targets stay for a hand release while the pipeline is not yet deployed.

The staging and production stacks keep their stack names and construct IDs, so the pipeline updates the stacks that exist today rather than creating new ones.

Alternatives considered: GitHub Actions with OIDC roles (MPD-513, the two existing workflows). Rejected on MPD-592: production deploys stay inside AWS, not in a public repository's workflows.

### D2. Configuration comes from SSM in the deployment account
The pipeline reads three String parameters, each a JSON object, in the deployment account in `ap-southeast-2`:

- `/sendrec/pipeline/settings`: `deploymentAccount` and `connectionArn`.
- `/sendrec/pipeline/staging-context`: the CDK context a staging release passes today (`activateMpdIntegration`, `mpdIntegrationSecretArn`, the `MPD_*` settings, `mpdProcessingProgress`, `mpdWatchShare`, `transcriptionProvider`).
- `/sendrec/pipeline/production-context`: the same for production plus the production inputs (`productionAccount`, `productionVpcId`, `productionCloudFrontPrefixListId`, `productionBucket`, `productionAlertTopicArn`, `productionDomainCertificateArn`, `productionTaskCount`, `productionDatabaseSize`).

Each context also names its network (`vpcAvailabilityZones`, `vpcPrivateSubnetIds`, `vpcIsolatedSubnetIds`), which the stack then imports with `Vpc.fromVpcAttributes` instead of a lookup. A CDK lookup role carries `ReadOnlyAccess`, which can read every bucket in its account, recordings included, and the Build step runs third-party build code, so the pipeline never assumes one. A hand deploy without those keys still looks the VPC up.

The Build step loads them through CodeBuild's `parameter-store` environment, so their values never appear in a command or a log. The app sets each object's keys as context on its own stage, so a staging value can never reach production. It refuses keys the pipeline owns (`releaseImage`, `productionImage`, `targetEnvironment`) and bootstrap mode. Every existing check still applies: production synthesis fails without `productionTaskCount`, and an active integration fails without both switches. A switch change is a parameter change followed by a pipeline run, never part of a release.

Alarm emails reuse MPD API's `/mpd-api/alarm-emails/{staging,production}`, as the staff app does.

Alternatives considered: committing the values as mpd-api does. Rejected: this repository is public.

### D3. The image is built once, in the Build step
The `Build` step, in the deployment account, builds the repository's `Dockerfile` for `linux/amd64` with `--build-arg VERSION=<first 8 characters of the commit>` and `--label org.opencontainers.image.revision=<commit>`, pushes it to staging's `sendrec` repository as `:<commit>`, reads the pushed digest back from ECR, and synthesizes both stages with it. If the repository already holds that tag, as when self-mutation restarts the run, it reuses the digest and builds nothing; the tag is immutable, so it cannot be another image. Building here, not in a later step, is the only way the stage templates can name the digest: CDK Pipelines synthesizes every stage in this step.

The app writes the release to `sendrec-release.json` in the cloud assembly (commit, version, digest and both image URIs). Later steps read the release from there, so no per-commit value enters the pipeline's own definition, and a merge does not make the pipeline update itself.

The step runs as the named role `sendrec-pipeline-build`, so staging's repository policies can name it: it pushes to `sendrec` and pulls the base image from `sendrec-base`. It assumes no role in another account. Before building it runs the Python, Node and Gradle infrastructure tests. A reused image must carry the commit in its revision label, so an image pushed by hand without it is refused.

### D4. Production copies the staging digest, never rebuilds
After the gate, a `PromoteImage` step runs before the production deploy as the named role `sendrec-pipeline-promote`. It copies `sendrec@<digest>` from staging's repository to production's as `:<commit>` with `crane`, which keeps the digest, and checks the copy's digest. If production already has that tag with the same digest it does nothing; with a different digest it fails. Production's repository policy lets only this role push. `crane` is a pinned release archive checked against its SHA-256 before it runs.

### D5. Migrations run inside each stack deploy, before the service
Each stack gets a custom resource, `MigrationRun`, backed by an inline Python Lambda (`migration-runner.py`). Its properties name the migration task definition revision, so a new image creates a new revision and CloudFormation calls the Lambda. The Lambda starts that definition once on Fargate in the service's subnets and security group, waits until it stops, and succeeds only on exit code 0. The service depends on the resource, so CloudFormation changes the service only after the migration succeeds. A failed or timed-out migration fails the deploy and rolls the stack back; the service never changes.

During a rollback CloudFormation calls the Lambda again with the previous definition. Running an older image's migrations against a newer schema would fail, so the Lambda reads its stack's status and does nothing while the stack is rolling back. Deleting the resource does nothing.

The Lambda stops the task and fails if it is still running a minute before the Lambda's 15-minute limit. Every SendRec migration so far has finished in under two minutes.

Alternatives considered:
- A pipeline step that runs the task through a role in each target account. Rejected: it needs a new role in both accounts before the first run, and the stack could still deploy after a failed step was retried by hand.
- The CDK `Provider` framework with an `isComplete` handler. Rejected: it adds file assets, which the hand release cannot publish.

### D6. Each deploy checks the version it shipped
After each stack deploy, a `Verify` step reads `/api/health` through the stack's `BaseUrl` output and requires `version` to equal the release's first 8 characters, retrying for up to 5 minutes. CloudFormation has already waited for the service to stabilise behind `/api/ready`, so this proves that the public URL serves the new build.

### D7. The nightly release is the staff app's, ported to Java
`NightlyRelease.java` is a line-for-line port of the staff app's `nightly_release.py` on `main` (which carries #618's fixes: confirmation reads, paging the run list, ordinary-run checks, one release per firing, no positional bindings), with SendRec's names:

| | Staff app | SendRec |
|---|---|---|
| Pipeline | `MpdOsStaff` | `SendRec` |
| State machine | `MpdOsStaffRelease` | `SendRecRelease` |
| Hold parameter | `/mpd-os-staff/release/hold` | `/sendrec/release/hold` |
| Topics | `mpd-os-staff-pipeline-{staging,production}` | `sendrec-pipeline-{staging,production}` |
| Time | 17:10 Australia/Sydney | 17:20 Australia/Sydney |

A port, not a reinvention: the decisions, their order, the read cap and the skip messages are the staff app's, and the tests run the synthesized definition against a fake pipeline in the same way.

### D8. 17:20 Sydney
mpd-api releases at 16:55, Cortex at 17:00 and the staff app at 17:10, taking about 3 minutes. SendRec follows at 17:20, so a SendRec change that needs a new mpd-api or staff app ships after both. SendRec has no consumer that must release after it.

### D9. The hold starts on
CDK never creates `/sendrec/release/hold`. The one-time steps create it as `on: first pipeline release not yet approved by hand`. The first production deploy through the pipeline is approved by hand in the maintenance window, and only after that is checked does an operator set `off`.

### D10. The GitHub workflows go
`mpd-build-candidate.yml` and `mpd-promote.yml` are deleted. They have never run, the roles and variables they need were never created, and leaving them invites a second, untested path to production from a public repository. `promote-release.py` and the hand runbook stay until the pipeline has made its first production release, as the fallback.

### D11. Staging waits for GitHub CI
The nightly release approves without a person, so the Go, web and end-to-end checks must have passed before a build reaches staging. As in the staff app, a `WaitForGitHubCI` step before the staging deploy reads `ci.yml`'s run on `main` for the commit with a read-only token (`sendrec/github-ci-read`) and fails unless it concluded `success`. The staff app's fallback that runs the checks in CodeBuild is left out: SendRec's checks need PostgreSQL and ffmpeg. If GitHub cannot run them, rerun the workflow and then the stage.

## Risks / Trade-offs

- **Docker Hub limits.** The Dockerfile's build stages pull `node` and `golang` from Docker Hub, and CodeBuild shares source addresses. A rate-limited pull fails `Build`, which alerts staging's list; rerunning the stage usually passes. A pull-through cache is a follow-up if it recurs.
- **GitHub outage.** While GitHub Actions cannot run `ci.yml`, nothing reaches staging.
- **A migration over 13 minutes fails the release.** Run such a migration by hand first, as today.
- **First deploy.** Staging and production must trust the deployment account in their CDK bootstrap, as they do for mpd-api. The first pipeline run updates both stacks; the runbook checks its diff before the gate is approved.

## Migration Plan

The README's one-time steps, in order: check both bootstraps trust the deployment account; create the three configuration parameters, the GitHub token and the hold; set the staging and production repository policies; `cdk deploy SendRecPipeline` as a deployment-account administrator; hold the first run before staging and read its diff; check the production diff; approve the first production deploy by hand; set the hold to `off`. Rollback before the first production approval: delete `SendRecPipeline`; staging keeps the image the pipeline deployed, and the hand release still works.
