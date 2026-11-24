## ADDED Requirements

### Requirement: Releases go through one pipeline in the deployment account
SendRec SHALL have a self-mutating CDK pipeline, `SendRec`, defined in this repository and deployed to the deployment account, whose source is `main` of this repository. Its stages SHALL be `Source`, `Build`, `UpdatePipeline`, `Staging`, `ApproveProduction` and `Production`, in that order. It SHALL update the existing `SendRecStaging` and `SendRecProduction` stacks.

#### Scenario: A merge reaches staging
- **WHEN** a commit is merged to `main`
- **THEN** the pipeline builds it, deploys `SendRecStaging` with it and waits at `ApproveProduction`

#### Scenario: Production waits for the gate
- **WHEN** a run has deployed staging and nobody has approved it
- **THEN** `SendRecProduction` is unchanged

### Requirement: One image per commit, built inside AWS
The `Build` step SHALL build the image in AWS with `VERSION` set to the commit's first 8 characters and the `org.opencontainers.image.revision` label set to the full commit, push it to staging's `sendrec` repository tagged with the full commit, and synthesize both stages with the pushed digest. When the repository already holds that tag, it SHALL reuse the tag's digest and build nothing.

#### Scenario: The pipeline restarts after updating itself
- **WHEN** a run restarts on a commit whose image is already in staging's repository
- **THEN** the step uses that image's digest and pushes nothing

### Requirement: Staging waits for passing checks
Before deploying staging, the pipeline SHALL wait for the result GitHub's `ci.yml` run on `main` recorded in the deployment account for the release's exact commit, and SHALL fail the stage unless that result is `success`. It SHALL use no GitHub token. Only a push to `main` of this repository, identified by GitHub's immutable subject, SHALL be able to record a result, and the recording role SHALL be able to do nothing else. The `Build` step SHALL run the infrastructure tests before building the image.

#### Scenario: CI fails on the merged commit
- **WHEN** `ci.yml` records `failure` for the commit
- **THEN** the `Staging` stage fails before deploying and its alert list is emailed

#### Scenario: The nightly release ships an older commit
- **WHEN** the run at the gate is for a commit older than main's latest, and both have recorded results
- **THEN** the wait step reads the older commit's own result

#### Scenario: Nothing is recorded
- **WHEN** no result is recorded for the commit within 60 minutes
- **THEN** the `Staging` stage fails

#### Scenario: A pull request or fork tries to record
- **WHEN** a workflow run that is not a push to `main` of this repository asks to assume the recording role
- **THEN** AWS refuses it

### Requirement: Production runs the staging digest
Before deploying production, the pipeline SHALL copy the staging image by digest into production's `sendrec` repository under the full commit, and SHALL fail if the copy's digest differs or the tag already names another digest. Production SHALL never be built from source.

#### Scenario: A release reaches production
- **WHEN** an approved run deploys production
- **THEN** production's task definitions name the same digest staging ran

#### Scenario: The production tag already names another image
- **WHEN** production's repository holds the commit's tag with a different digest
- **THEN** the copy step fails and production is not deployed

### Requirement: Migrations run before the service changes
Each SendRec stack deploy SHALL run the release's migration task definition once, after the database exists and before the service changes, and SHALL fail the deploy unless the task exits 0. The application SHALL keep `MIGRATIONS_MODE=skip`. A stack rollback SHALL NOT run migrations.

#### Scenario: A migration fails
- **WHEN** the migration task exits non-zero during a release
- **THEN** the stack deploy fails, the stack rolls back, and the service keeps its previous task definition

#### Scenario: An older release is deployed again
- **WHEN** release R2 added a migration and R1, whose image lacks it, is deployed again by stage rollback, revert or hand
- **THEN** R1's migration task exits 0 without changing the schema and the deploy continues

#### Scenario: A migration overruns
- **WHEN** the migration task is still running after 14 minutes
- **THEN** the deploy fails and rolls back, and the task is left to finish

#### Scenario: A release rolls back
- **WHEN** a deploy fails after its migration succeeded and CloudFormation rolls the stack back
- **THEN** the migration runner does nothing and the rollback completes

### Requirement: Deploys prove the version they shipped
After each stack deploy the pipeline SHALL check that the stack's public `/api/health` reports the release's version, and fail the stage if it does not within 5 minutes.

#### Scenario: The old build still answers
- **WHEN** `/api/health` keeps reporting the previous version for 5 minutes after a deploy
- **THEN** the stage fails and its alert list is emailed

### Requirement: No identifying production detail in the repository
Production's account, network, storage, certificate, alert destination, task count, switches and integration settings SHALL come from SSM parameters in the deployment account when the pipeline synthesizes, not from committed files. Production synthesis SHALL still fail without `productionTaskCount`. The pipeline's configuration SHALL NOT set the release image or bootstrap mode, and SHALL name each stage's network so that the pipeline assumes no role in the staging or production account.

#### Scenario: The production configuration leaves out the task count
- **WHEN** `/sendrec/pipeline/production-context` has no `productionTaskCount`
- **THEN** the `Build` step fails and nothing deploys

#### Scenario: A configuration names an image
- **WHEN** a context parameter sets `releaseImage` or `productionImage`
- **THEN** synthesis fails

### Requirement: Nightly production release at 17:20 Sydney
A schedule SHALL start the release state machine at 17:20 `Australia/Sydney` every day. The release SHALL approve the newest staging-verified run waiting at `ApproveProduction` only while `/sendrec/release/hold` holds exactly `off`, SHALL reject older runs waiting at the gate whenever `Staging` succeeds, and SHALL apply the staff app's checks before approving: transitions enabled, nothing deploying to production, the pipeline unchanged between the read that chose the run and the read that confirms it. One firing SHALL start at most one release. CDK SHALL NOT create or change the hold parameter.

#### Scenario: Hold is off and a run waits
- **WHEN** the hold is `off` and the newest staging-verified run waits at the gate at 17:20
- **THEN** the release approves it with the summary `Nightly release 17:20 Australia/Sydney`

#### Scenario: Hold is not off
- **WHEN** the hold is missing or holds anything but `off`
- **THEN** the release approves nothing and emails the production list naming the hold

#### Scenario: A newer run passes staging
- **WHEN** a newer run succeeds in `Staging` while an older run waits at the gate
- **THEN** the older run is rejected with the summary `Superseded by <newer run>`

### Requirement: Failures alert
A failed `Source`, `Build`, `UpdatePipeline` or `Staging` stage SHALL email the staging list through `sendrec-pipeline-staging`. A failed `Production` stage, a failed, timed-out or aborted release job and a held release SHALL email the production list through `sendrec-pipeline-production`. Only rules in the deployment account SHALL publish to those topics.

#### Scenario: Production deploy fails
- **WHEN** the `Production` stage fails
- **THEN** the production list gets an email linking the failed run

### Requirement: No production path in GitHub workflows
The repository SHALL NOT keep GitHub workflows that build release candidates for, or deploy to, MPD's AWS accounts.

#### Scenario: Listing the workflows
- **WHEN** the repository's workflows are listed
- **THEN** neither `mpd-build-candidate.yml` nor `mpd-promote.yml` exists
