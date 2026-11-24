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

During a rollback CloudFormation calls the Lambda again with the previous definition, so the Lambda reads its stack's status and does nothing while the stack is rolling back. Deleting the resource does nothing.

A forward deploy of an older release (a stage rollback, a revert, or redeploying the previous template) runs the older image's migration task against a newer schema. golang-migrate stops there with "no migration found for version N", which would fail the deploy and keep the release being rolled back. So `MIGRATIONS_MODE=only` first compares the database's version with the image's newest migration: when the database is clean and newer, it logs that and exits 0 without changing anything. A dirty database still reaches golang-migrate, which refuses it. Migrations are never reversed; SendRec's only add tables, columns and indexes, which older code ignores. Images built before this change lack the check, so a rollback must not go past the first release that has it.

If the task is still running a minute before the Lambda's 15-minute limit, the Lambda fails the deploy and leaves the task to finish: stopping a migration midway marks the schema dirty and can leave an invalid index, which blocks every later release until it is repaired by hand (README, "Recovering a dirty schema"). Every SendRec migration so far has finished in under two minutes.

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

It also carries cortex-ai #132's four fixes to that logic, found when Cortex ported it: with the gate idle, a `ReadGate` choice decides before the run list is read and waits only for an unfinished run to enter, so a cancelled run still shown waiting no longer fails every release; the approval snapshot covers only Staging, the gate and Production, so a merge moving through Build does not skip the night; the gate's token is read only from a waiting approval; and a confirmation that finds the pipeline changed reads again within the cap instead of skipping.

### D8. 17:20 Sydney
mpd-api releases at 16:55, Cortex at 17:00 and the staff app at 17:10, taking about 3 minutes. SendRec follows at 17:20, so a SendRec change that needs a new mpd-api or staff app ships after both. SendRec has no consumer that must release after it.

### D9. The hold starts on
CDK never creates `/sendrec/release/hold`. The one-time steps create it as `on: first pipeline release not yet approved by hand`. The first production deploy through the pipeline is approved by hand in the maintenance window, and only after that is checked does an operator set `off`.

### D10. The GitHub workflows go
`mpd-build-candidate.yml` and `mpd-promote.yml` are deleted. They have never run, the roles and variables they need were never created, and leaving them invites a second, untested path to production from a public repository. `promote-release.py` and the hand runbook stay until the pipeline has made its first production release, as the fallback.

### D11. Staging waits for GitHub CI, which GitHub records in AWS
The nightly release approves without a person, so the Go, web and end-to-end checks must have passed before a build reaches staging. Sami decided on 24 November 2026 that the pipeline proves this without any GitHub token, the way Command Centre deploys: GitHub pushes the result into AWS, and the pipeline reads it there.

- `ci.yml` gains a last job, `record-result`. It runs only on a push to `main` of this repository, after every check job, with `id-token: write` in that job alone. It signs in through GitHub OIDC with `aws-actions/configure-aws-credentials` pinned to a full commit SHA, the account id masked and the role ARN from the Actions secret `SENDREC_CI_RECORD_ROLE_ARN`. GitHub prints a step's inputs in this public repository's logs, and a variable would show the account id there; a secret is masked. The job records `success` when every check passed and `failure` otherwise, so a red commit stops the pipeline at once instead of at the timeout.
- The role, `sendrec-github-ci-record`, trusts the deployment account's `token.actions.githubusercontent.com` provider with exact matches on audience `sts.amazonaws.com` and on one subject, `repo:My-Performance-Doctor@199071226/sendrec@1408816431:ref:refs/heads/main`. That is GitHub's immutable subject, which this repository already uses: the ids survive a rename, so a repository that later takes the name cannot assume the role. Pull requests, forks, other branches, tags and environments sign with other subjects, and a fork's pull request gets no token. The role's only permission is `dynamodb:PutItem` on the results table.
- **Record shape: one DynamoDB item per commit** in `sendrec-ci-results`, keyed by the full SHA, holding the conclusion, each check's result, the run's link and attempt, and a 90-day expiry through the table's TTL. The nightly release ships the newest staging-verified run, often hours after its CI passed and after newer merges recorded theirs, so `WaitForGitHubCI` looks up its own commit with a consistent `GetItem`; a single "latest result" value would answer for the wrong commit. A rerun overwrites the commit's item. Rejected an SSM parameter per commit: parameters never expire and count against the account's limit, shared with every other app there.
- **The OIDC provider** is one per URL per account. The settings parameter's optional `githubOidcProviderArn` imports an existing provider; without it the stack creates one with the native `AWS::IAM::OIDCProvider` resource, which needs no Lambda or file asset.
- `WaitForGitHubCI` has no secret: it reads with `dynamodb:GetItem` on that table only. It passes on a recorded `success` for the exact commit, fails at once on any other recorded conclusion, and fails when nothing is recorded within 60 minutes.

Rejected: a fine-grained read token (`sendrec/github-ci-read`), which needs an organisation owner's approval and renewal before it expires; calling GitHub's API without a token, whose limit of 60 requests an hour is shared by every build on the same CodeBuild address; and dropping the check, which lets an unattended nightly release ship a commit whose tests failed. The staff app's fallback that runs the checks in CodeBuild is left out: SendRec's checks need PostgreSQL and ffmpeg. If GitHub cannot run them, rerun the workflow and then the stage.

## Risks / Trade-offs

- **Docker Hub limits.** The Dockerfile's build stages pull `node` and `golang` from Docker Hub, and CodeBuild shares source addresses. A rate-limited pull fails `Build`, which alerts staging's list; rerunning the stage usually passes. A pull-through cache is a follow-up if it recurs.
- **GitHub outage.** While GitHub Actions cannot run `ci.yml`, nothing reaches staging.
- **A migration over 13 minutes fails the release.** Run such a migration by hand first, as today.
- **First deploy.** Staging and production must trust the deployment account in their CDK bootstrap, as they do for mpd-api. The first pipeline run updates both stacks; the runbook checks its diff before the gate is approved.

## Migration Plan

The README's one-time steps, in order: check both bootstraps trust the deployment account; create the three configuration parameters, the GitHub token and the hold; set the staging and production repository policies; `cdk deploy SendRecPipeline` as a deployment-account administrator; hold the first run before staging and read its diff; check the production diff; approve the first production deploy by hand; set the hold to `off`. Rollback before the first production approval: delete `SendRecPipeline`; staging keeps the image the pipeline deployed, and the hand release still works.
