# SendRec staging

This stack targets account `537421187871` in `ap-southeast-2` using the existing staging VPC. CloudFront supplies a trusted `.cloudfront.net` HTTPS hostname. Its VPC origin reaches an internal ALB in isolated subnets. Fargate runs in private subnets with egress, and the dedicated encrypted PostgreSQL database runs in isolated subnets. S3 recordings remain private. Application responses use the managed CachingDisabled policy.

The operator approved synthetic evaluation and requested an alternative to external DNS. This deployment does not use the pending regional certificate or require a domain CNAME. A deploy can add a custom hostname for patient-facing links; see [Custom hostname](#custom-hostname).

## Checks

Use Java 21, Docker, Node, Python 3, the AWS CLI and CDK CLI. Set `JAVA_HOME` to the installed Java 21 directory and include its `bin` in `PATH`. From the repository root:

```bash
python3 -B -m unittest discover -s infrastructure/tests -v
cd infrastructure
node tests/test_bootstrap_access.cjs
./gradlew test
export AWS_PROFILE=mpd-staging
aws sts get-caller-identity
python3 cdk-with-session.py synth SendRecStaging
```

The CDK helper loads short-lived credentials through the AWS CLI because the installed CDK SDK does not resolve this machine's SSO session. It keeps credentials in memory and child-process environment only. Set `AWS_PROFILE` and `JAVA_HOME` before invoking it.

Run the Go suite with the toolchain in `go.mod`, ffmpeg and a dedicated synthetic PostgreSQL 18 database through `TEST_DATABASE_URL`. SendRec uses `RETURNING old/new` when publishing thumbnails and transcripts, so PostgreSQL 17 can pass migrations and health checks but fails media processing. The stack uses RDS PostgreSQL 18.6, a verified upgrade target in Sydney. Major upgrades retain the instance and take pre-upgrade and post-upgrade snapshots under the seven-day backup policy. Build `web/dist` first. Never point tests at patient databases. CDK builds the repository Dockerfile and pushes the asset to its existing staging bootstrap registry.

## Runtime base image

The runtime stage builds `FROM` MPD's own build of `Dockerfile.base` (whisper.cpp, ffmpeg, the RNNoise model and Alpine's CA store), pinned by digest in the `Dockerfile`'s `BASE_IMAGE` argument. It lives in this account's ECR repository `sendrec-base`, which has immutable tags and scans on push.

Before it builds the image, cdk-assets logs Docker in to this registry as the CDK image-publishing role, replacing any login of your own. The repository policy in `sendrec-base-repository-policy.json` lets that role pull the base, so a CDK deploy needs no extra login. Reapply the policy if the repository is recreated:

```bash
aws ecr set-repository-policy --region ap-southeast-2 --repository-name sendrec-base --policy-text file://sendrec-base-repository-policy.json
```

Once `SendRecPipeline` exists, apply `pipeline/staging-base-repository-policy.json` instead, as in step 4 of the pipeline's one-time setup. It holds the same statement plus the pipeline build role's pull, and the policy above alone would stop the pipeline building images.

Log Docker in yourself for a local build against the pinned base:

```bash
aws ecr get-login-password --region ap-southeast-2 | docker login --username AWS --password-stdin 537421187871.dkr.ecr.ap-southeast-2.amazonaws.com
```

`Dockerfile.base`, `Dockerfile` and `Dockerfile.standalone` all run `scripts/check-ca-roots.sh`. It fails the build if the CA store holds `emSign ECC Root CA - G3`, `emSign Root CA - C1` or `emSign ECC Root CA - C3`, the roots Alpine removed after the 2026 eMudhra intermediate compromise, or `AAA Certificate Services`. The `Dockerfile` checks the store it ships, so a stale or outside base fails there too. To rebuild the base, for a new whisper.cpp or Alpine CA update, build `Dockerfile.base` without cache from the repository root, push it under a new dated tag and put the pushed digest in `BASE_IMAGE`. Without `--pull --no-cache` a cached Alpine layer can carry the old CA store into the new tag:

```bash
docker build --pull --no-cache --platform linux/amd64 -f Dockerfile.base -t 537421187871.dkr.ecr.ap-southeast-2.amazonaws.com/sendrec-base:whisper-<version>-alpine<release>-<yyyymmdd> .
docker push 537421187871.dkr.ecr.ap-southeast-2.amazonaws.com/sendrec-base:whisper-<version>-alpine<release>-<yyyymmdd>
```

CI cannot read this registry, so it builds `Dockerfile.base` itself and passes that local tag as `BASE_IMAGE`. `make docker-up` does the same for local stacks.

The stack sets `BRAND_FONT_DIR` to `/usr/share/mpd-brand/fonts`. Patient pages load TT Neoris DemiBold from `tt-neoris-demibold.ttf` there and fall back to Geist without it. The font's licence keeps it out of this repository, so add it as a layer over the pinned base, built from a private directory, push that under a new tag and pin its digest in `BASE_IMAGE`:

```dockerfile
FROM 537421187871.dkr.ecr.ap-southeast-2.amazonaws.com/sendrec-base@sha256:<current base digest>
COPY --chmod=644 tt-neoris-demibold.ttf /usr/share/mpd-brand/fonts/tt-neoris-demibold.ttf
```

Use only a file MPD holds a web licence for. The copy in Staff OS is a trial build, so the current base carries no font. A base rebuilt from `Dockerfile.base` loses this layer until it is added again.

## First owner

Verify the source machine's public IPv4 address and use only that `/32`. Bootstrap mode without a valid address fails before synthesis. Substitute that non-secret CIDR in the commands below:

```bash
python3 cdk-with-session.py synth SendRecStaging -c bootstrap=true -c bootstrapCidr=<setup-ip>/32
python3 cdk-with-session.py diff SendRecStaging -c bootstrap=true -c bootstrapCidr=<setup-ip>/32
python3 cdk-with-session.py deploy SendRecStaging -c bootstrap=true -c bootstrapCidr=<setup-ip>/32 --require-approval never --outputs-file /tmp/sendrec-outputs.json
```

Check the deployed viewer function with allowed and denied IP events. Verify HTTPS and `/api/health`, then register only a synthetic owner through the managed hostname. Passwords, session tokens and signed URLs must stay in private local files or Secrets Manager. Never pass them in shell arguments, print them or commit them. Email delivery is unconfigured during evaluation.

Close registration while retaining the source restriction:

```bash
python3 cdk-with-session.py deploy SendRecStaging -c bootstrap=false -c bootstrapCidr=<setup-ip>/32 --require-approval never
```

Wait for the replacement ECS task to become healthy. Verify anonymous registration fails and the existing owner can log in. Only then remove the source restriction:

```bash
python3 cdk-with-session.py deploy SendRecStaging -c bootstrap=false --require-approval never
```

Do not combine those two deployments. CloudFront updates can finish before an ECS task replacement, so changing both at once could expose registration.

## Custom hostname

Watch links read `{BASE_URL}/{shareToken}`. Staging moves from the CloudFront hostname to `results-staging.mpd.health`, and production uses `results.mpd.health`. `mpd.health` is hosted at GoDaddy, not Route 53.

1. Request a DNS-validated ACM certificate for the hostname in `us-east-1`. CloudFront accepts no other region. The developer SSO role is denied ACM in `us-east-1`, so an administrator requests it.
2. Add its validation CNAME at GoDaddy and wait for the certificate to be issued.
3. Add a CNAME from the hostname to the distribution's `*.cloudfront.net` name (`d2oopdll9i8yvf.cloudfront.net` for staging). It answers with a certificate error until step 4 deploys.
4. In a reviewed pull request, override `customDomain()` in `StagingConfig`:

```java
@Override
public CustomDomain customDomain() {
  return new CustomDomain("results-staging.mpd.health", "arn:aws:acm:us-east-1:537421187871:certificate/<id>");
}
```

5. After merge, diff and deploy staging with the current approved access contexts. The diff should change the distribution's aliases and certificate, `BASE_URL`, and the recordings bucket's CORS origins, and nothing else.

The hostname is committed config rather than a deploy context on purpose: a deploy that left it out would take the alias off the distribution, and every link already sent on the new hostname would fail.

Production commits its hostname, `results.mpd.health`, in `ProductionConfig`. Its certificate is the required deploy input `productionDomainCertificateArn`, like production's account and other values, so the public repository names no production account. A production deploy without it fails synthesis rather than dropping the alias.

In the same window, deploy mpd-api with `SENDREC_ORIGIN` set to `https://results-staging.mpd.health` and its `sendrecCallbackUrl` set to `https://results-staging.mpd.health/api/auth/mpd/callback`. mpd-api refuses metadata whose `watchUrl` origin differs from `SENDREC_ORIGIN`, so sends fail closed until both are deployed. The Cognito client takes the callback from `sendrecCallbackUrl`; without it, staff sign-in to SendRec's own pages fails.

Deploy Staff OS in the same window too, with its SendRec URL on the new hostname. Staff OS frames `/mpd-preview` from its configured SendRec URL, and the framed page redeems its handoff only from `BASE_URL`. A frame still on the CloudFront hostname answers `preview origin denied`. Staff OS's session and API calls need no change, because SendRec allow-lists the Staff OS origin rather than comparing it with `BASE_URL`.

The CloudFront hostname keeps answering, so `/watch/{shareToken}` links sent earlier still open there, and the bucket's CORS rule keeps that origin for their captions. SendRec's own staff sign-in accepts only `BASE_URL` as its origin, so staff use the new hostname for SendRec's pages.

## Verify and recover

Use synthetic speech and video to check direct signed S3 upload, unsigned-object denial, playback, password denial with a successful authorized control, nonempty local VTT and webhook signature acceptance with tamper rejection. Do not configure a receiver that sends real messages. Replace the service task and verify the video, database entry and transcript persist. Record the exact revision, outputs, template hash and results in `DEPLOYMENT.md`. A runbook is not deployment evidence.

RDS takes daily backups retained for seven days. One Fargate task runs at a time, so a deployment or replacement interrupts service briefly. The model is pinned and checksum-verified at task startup. Concurrent ffmpeg encodes are limited to one. Whisper runs at the lowest CPU priority, so an encode takes the CPU first, and a transcript may take 30 minutes plus four times the recording's length. The small Whisper model is an evaluation choice, not approval of clinical transcription accuracy.

A task that is stopping, as in a deploy or a scale-in, hands its running transcription back to the queue at once. The worker requeues any other abandoned `processing` job one minute after its deadline: 30 minutes plus four times the recording's length, with the added time capped at 4 hours, so 71 minutes for a 10-minute recording. To recover sooner, use the owner's retranscribe action, backed by authenticated `POST /api/videos/<video-id>/retranscribe`. A successful retry returns 202. Verify the job reaches `ready` and its signed VTT contains cues before calling recovery complete. The staging task-replacement check uses this explicit retry; it does not claim to have waited for the automatic stale-job path.

To recover a bad application update, check out the last verified revision and deploy it with the current approved access contexts. `DEPLOYMENT.md` records the current staging `MPD_ALLOWED_ORIGINS`. A deploy that leaves out an origin removes it from the service and from the recordings bucket's CORS rule. Read the CloudFormation diff first. Do not reset a dirty checkout or replace retained state. A failed initial deployment needs resource and failure readback before any cleanup.

## Retained resources

Stack deletion retains the recordings bucket, database instance and all Secrets Manager secrets. They remain billable until an operator explicitly removes them. The interrupted HTTP draft created an unencrypted `sendrec-staging-db`. The HTTPS update uses `sendrec-staging-encrypted-db`, retaining the old database instead of deleting unknown state. Record both resources in the deployment handoff and leave cleanup to an operator. The controlled Lambda receiver verifies synthetic webhook HMAC signatures and returns 403 for tampering. It never forwards messages or logs payloads. Store the application-generated synthetic webhook secret in its dedicated Secrets Manager secret before testing. An IAM Lambda invocation can also probe the restricted application from an outside source IP.

The ECS service, private ALB, CloudFront distribution, viewer function, synthetic receiver and log group follow the stack lifecycle. The existing VPC and CDK asset registry are shared and must remain untouched.

Before any operator-run teardown, list the stack resources, verify backups and export the synthetic evaluation record. Deleting the stack does not delete retained recordings or the database. Do not automate their deletion or delete the shared bootstrap registry.

## Production preparation

The optional production stack and immutable release controls are documented in [the production runbook](../docs/production/README.md). Production runs the number of application tasks named by the required `productionTaskCount` input (1 to 4, normally 2), with integration flags disabled by default.

## Deployment pipeline

`SendRecPipeline` (`PipelineStack.java`, OpenSpec change `add-deploy-pipeline`) runs in MPD's deployment account, like the pipelines of MPD API, the staff app and Cortex. Each merge to `main` runs, in order:

1. **Source**: `main` through the deployment account's GitHub connection.
2. **Build** (`Synth`, role `sendrec-pipeline-build`): the Python, Node and Gradle infrastructure tests, then `pipeline/build-image.sh` builds the image for `linux/amd64` with `VERSION` set to the commit's first 8 characters and the `org.opencontainers.image.revision` label, and pushes it to staging's `sendrec` repository as `:<commit>`. A commit already there is not built again. Then `cdk synth -c targetEnvironment=pipeline` synthesizes both stages with the pushed digest and writes `sendrec-release.json` into the cloud assembly.
3. **UpdatePipeline**: the pipeline updates itself when its own definition changed. A merge alone never changes it, because no per-commit value enters it.
4. **Staging**: `WaitForGitHubCI` waits until GitHub's `ci.yml` run on `main` has recorded a pass for the commit (the Go, web and end-to-end checks; see [CI result record](#ci-result-record)), then the stage deploys `SendRecStaging` with the digest, then `VerifyStaging` waits up to 5 minutes for `/api/health` to report the version.
5. **ApproveProduction**: a manual approval, which the nightly release approves.
6. **Production**: `PromoteImage` (role `sendrec-pipeline-promote`) copies the digest into production's `sendrec` repository with a pinned, checksum-checked `crane`; production never rebuilds. Then it deploys `SendRecProduction` and `VerifyProduction` checks the version.

Every stack deploy with a release image runs the migration task definition first, through the `MigrationRun` custom resource (`migration-runner.py`): after the database exists and before the service changes. A migration that exits non-zero fails the deploy; the stack rolls back and the service keeps its old definition. One still running after 14 minutes fails the deploy the same way but is left to finish, because stopping it midway would leave the schema dirty. A CloudFormation rollback runs nothing. Redeploying an older release, by stage rollback, revert or hand, runs that image's migration task, which finds the database newer than its own newest migration and exits 0 without changing anything: the schema stays at the newer release's version. The older image's migration run finds the database newer than its own newest migration and exits 0 without changing anything, so the older code runs on the newer schema; SendRec's migrations only add tables, columns and indexes, which older code ignores. Only images built from the `add-deploy-pipeline` change onward skip like this. An older image stops with "no migration found for version N", so never roll back past the first release that carries the change. The app keeps `MIGRATIONS_MODE=skip`.

### Recovering a dirty schema

A migration that fails partway leaves `schema_migrations` dirty, and every later release then fails at `MigrationRun` with "Dirty database version N". Recovery happens under a grant for production, and with synthetic data only on staging:

1. Read the migration task's log (`migration/` stream in the stack's log group) for the failing statement.
2. With a one-off task against the private database (the "Staging database without a login" pattern in `docs/agents/claude/production-release.md`), read `SELECT version, dirty FROM schema_migrations`.
3. Undo what the failed migration left half done: for an interrupted `CREATE INDEX CONCURRENTLY`, `DROP INDEX CONCURRENTLY IF EXISTS <index>`, because `IF NOT EXISTS` would keep the invalid index. Migrations 000083 and 000084 have their own steps in `DEPLOYMENT.md`.
4. `UPDATE schema_migrations SET version = <N - 1>, dirty = false`.
5. Release again (**Release change**, or **Retry** on the failed stage); the migration runs from N.

### Configuration

Nothing that identifies production is committed. The Build step reads these String parameters in the deployment account, `ap-southeast-2`:

| Parameter | Holds |
|---|---|
| `/sendrec/pipeline/settings` | `{"deploymentAccount": "…", "connectionArn": "…"}`, the same GitHub connection MPD API's pipeline uses, plus `"githubOidcProviderArn": "…"` when the deployment account already has GitHub's OIDC provider |
| `/sendrec/pipeline/staging-context` | the CDK context a staging release passes: `activateMpdIntegration`, `mpdIntegrationSecretArn`, the nine `MPD_*` settings, `mpdProcessingProgress`, `mpdWatchShare`, and `transcriptionProvider` if not local; plus the network, below |
| `/sendrec/pipeline/production-context` | the same for production, plus `productionAccount`, `productionVpcId`, `productionCloudFrontPrefixListId`, `productionBucket`, `productionAlertTopicArn`, `productionDomainCertificateArn`, `productionTaskCount` and `productionDatabaseSize`; plus the network, below |
| `/mpd-api/alarm-emails/staging`, `/mpd-api/alarm-emails/production` | MPD API's alarm lists, which the staff app also uses |
| GitHub Actions secret `SENDREC_CI_RECORD_ROLE_ARN` | the ARN of `sendrec-github-ci-record`, which `ci.yml` assumes to record its result; a secret so the account id stays out of the public logs |

Each context also names its network, so the pipeline needs no CDK lookup role in staging or production (a lookup role can read every bucket in its account, recordings included): `vpcAvailabilityZones`, `vpcPrivateSubnetIds` and `vpcIsolatedSubnetIds`, comma-separated, one subnet of each kind per zone, in zone order. Read them once from the VPC the stack uses, or from the live stack's `PrivateSubnetIds` output and its database subnet group.

Each context is a flat JSON object of strings, numbers and booleans, and reaches only its own stage. Synthesis refuses `releaseImage`, `productionImage`, `targetEnvironment`, `bootstrap` and `bootstrapCidr`: the pipeline sets the images and never bootstraps. Production still fails without `productionTaskCount`, and an active integration without both switches. To switch `mpdProcessingProgress` or `mpdWatchShare`, change the parameter in its own recorded step, then run the pipeline (**Release change**); a release never switches them. A parameter over 4 KB needs `--tier Advanced`.

### CI result record

The pipeline proves that GitHub CI passed without any GitHub token, the way Command Centre deploys: GitHub pushes the result into AWS. `ci.yml`'s last job, `record-result`, runs only on a push to `main` of this repository, after every check. It signs in through GitHub OIDC as `sendrec-github-ci-record` and puts one item in the DynamoDB table `sendrec-ci-results`: the commit, `success` when every check passed or `failure` otherwise, the checks' results, the run's link and an expiry 90 days out. `WaitForGitHubCI` reads the item for the exact commit it is releasing, with `dynamodb:GetItem` only. It passes on `success`, fails at once on `failure`, and fails after 60 minutes with nothing recorded.

- **Trust.** The role trusts the account's `token.actions.githubusercontent.com` provider, audience `sts.amazonaws.com`, and exactly one subject, `repo:My-Performance-Doctor@199071226/sendrec@1408816431:ref:refs/heads/main`: GitHub's immutable subject, by the organisation's and repository's ids (the repository uses it already). Pull requests, forks, other branches, tags and environments sign with other subjects, and fork pull requests get no token at all. AWS matches only the audience and subject, so any workflow running on `main` that asks for a token could assume the role. Only `record-result` asks: a test fails if any other workflow requests `id-token: write`, and adding one takes a reviewed merge to `main`, which can change what ships anyway.
- **Permission.** `dynamodb:PutItem` on that table, nothing else. It cannot deploy, read a secret or touch a parameter.
- **One item per commit.** The nightly release ships an older run hours after its CI passed, after newer merges recorded theirs, so it looks up its own commit, not the latest result. A rerun of `ci.yml` writes the item again.
- **A commit with no record** (the secret unset, a run from before the job existed, or an item past its 90-day expiry) never reaches staging. A rerun uses the workflow file of the run's own commit, and GitHub reruns only runs under 30 days old, so rerunning `ci.yml` records a result only for a commit that carries `record-result` and whose run is that recent; rerun it, then **Retry** the `Staging` stage. For any other commit, such as a rollback to an older release, check its `ci.yml` run passed, then put its item by hand with deploy rights in the deployment account (`aws dynamodb put-item --table-name sendrec-ci-results --item '{"commit":{"S":"<full commit>"},"conclusion":{"S":"success"},"runUrl":{"S":"<run link>"}}'`) and **Retry** the stage.
- **The old token.** `WaitForGitHubCI` no longer reads `sendrec/github-ci-read`. If that secret exists in the deployment account, delete it and revoke the token in GitHub.

### Nightly production release

The release is the staff app's, ported line for line (`NightlyRelease.java`; see the staff app's README, "Nightly production release", for every check). Its synthesized state machine is the staff app's with the names below substituted.

- **17:20 Sydney.** EventBridge Scheduler starts `SendRecRelease` at 17:20 `Australia/Sydney` every day, after MPD API (16:55), Cortex (17:00) and the staff app (17:10). It approves the run waiting at `ApproveProduction`. Merge by about 16:50 to make that day's release; the Build step takes longer than the staff app's because it builds the image.
- **Superseded runs.** Whenever `Staging` succeeds, older runs waiting at the gate are rejected with `Superseded by <execution id>`, so the gate holds the newest staging-verified run. Rejections send no alert.
- **Hold.** `/sendrec/release/hold` in the deployment account. Only the exact value `off` releases; anything else, or no parameter, holds and emails the production list every day. CDK never creates or changes it.

  ```bash
  aws ssm put-parameter --name /sendrec/release/hold --type String --overwrite \
    --value "on: <reason>" --region ap-southeast-2 --profile <deployment-admin>
  aws ssm put-parameter --name /sendrec/release/hold --type String --overwrite \
    --value off --region ap-southeast-2 --profile <deployment-admin>
  ```

- **Arrows.** Disabling the transition into `ApproveProduction` pauses releases; disabling the one into `Production` stops them, including a manual approval. The skip email quotes the reason.
- **Manual release.** Approve the gate in the console for a hotfix. Starting `SendRecRelease` with `{"mode":"release"}` also releases.
- **Alerts.** A failed `Source`, `Build`, `UpdatePipeline` or `Staging` stage emails the staging list through `sendrec-pipeline-staging`. A failed `Production` stage, a failed or timed-out release job and a held release email the production list through `sendrec-pipeline-production`. A deploy of `SendRecPipeline` without the two `-c alarmEmails…` values removes every subscription.

### One-time setup, in a maintenance window

These need deploy rights in the deployment account (a DeploymentMaintenance grant), staging ECR rights, and production ECR rights (a ProductionMaintenance grant). Keep the parameter files private and outside the repository.

1. **Check, read-only.** In staging and production, the `CDKToolkit` stack's `TrustedAccounts` parameter names the deployment account (MPD API's pipeline already needs this). The deployment account's GitHub connection is `AVAILABLE` and its GitHub app can read `My-Performance-Doctor/sendrec`; if not, an organisation owner adds the repository to the app in GitHub. `/mpd-api/alarm-emails/staging` and `/mpd-api/alarm-emails/production` exist.
2. **Configuration.** In a private directory `$PRIVATE_DIR`, write the three JSON files from the values the last release used (the staging task definition and the production release kit), with production's switches and task count as production runs them today, then create the parameters:

   ```bash
   for name in settings staging-context production-context; do
     aws ssm put-parameter --name "/sendrec/pipeline/$name" --type String \
       --value "file://$PRIVATE_DIR/$name.json" --region ap-southeast-2 --profile <deployment-admin>
   done
   ```

   **GitHub's OIDC provider.** An account holds one provider per URL. Check for it before the settings parameter is written, and before any merge that updates an existing `SendRecPipeline`:

   ```bash
   aws iam list-open-id-connect-providers --profile <deployment-admin> \
     --query "OpenIDConnectProviderList[?ends_with(Arn, '/token.actions.githubusercontent.com')].Arn" --output text
   ```

   If it prints an ARN, add `"githubOidcProviderArn": "<that ARN>"` to `settings.json`, and the stack imports it. If it prints nothing, leave the key out, and the stack creates the provider. Getting this wrong fails the deploy, not the trust: a second provider for the same URL is refused.

3. **Hold, starting on.**

   ```bash
   aws ssm put-parameter --name /sendrec/release/hold --type String \
     --value "on: first pipeline release not yet approved by hand" --region ap-southeast-2 --profile <deployment-admin>
   ```

4. **Repository policies.** The templates in `pipeline/` name the two pipeline roles through `aws:PrincipalArn`, so they work before the roles exist. Read each repository's current policy first: `set-repository-policy` replaces it, so merge any statement it holds into the template. `sendrec-base`'s should be the committed `sendrec-base-repository-policy.json`, which its template keeps. Set `DEPLOYMENT_ACCOUNT` to the deployment account's ID.

   ```bash
   policy() { sed "s/DEPLOYMENT_ACCOUNT/$DEPLOYMENT_ACCOUNT/g" "pipeline/$1"; }
   aws ecr get-repository-policy --repository-name sendrec --region ap-southeast-2 --profile <staging-admin>
   aws ecr set-repository-policy --repository-name sendrec --policy-text "$(policy staging-repository-policy.json)" \
     --region ap-southeast-2 --profile <staging-admin>
   aws ecr get-repository-policy --repository-name sendrec-base --region ap-southeast-2 --profile <staging-admin>
   aws ecr set-repository-policy --repository-name sendrec-base --policy-text "$(policy staging-base-repository-policy.json)" \
     --region ap-southeast-2 --profile <staging-admin>
   aws ecr get-repository-policy --repository-name sendrec --region ap-southeast-2 --profile <production-admin>
   aws ecr set-repository-policy --repository-name sendrec --policy-text "$(policy production-repository-policy.json)" \
     --region ap-southeast-2 --profile <production-admin>
   ```

5. **Deploy the pipeline** from a checkout of `main`, as a deployment-account administrator. `releaseDigest` only feeds the stage templates, which this command does not deploy; use the digest staging runs.

   ```bash
   cd infrastructure
   param() { aws ssm get-parameter --name "$1" --query Parameter.Value --output text \
     --region ap-southeast-2 --profile <deployment-admin>; }
   export SENDREC_PIPELINE_SETTINGS="$(param /sendrec/pipeline/settings)"
   export SENDREC_STAGING_CONTEXT="$(param /sendrec/pipeline/staging-context)"
   export SENDREC_PRODUCTION_CONTEXT="$(param /sendrec/pipeline/production-context)"
   cdk deploy SendRecPipeline --profile <deployment-admin> -c targetEnvironment=pipeline \
     -c releaseRevision="$(git rev-parse HEAD)" -c releaseDigest=<staging's current digest> \
     -c alarmEmailsStaging="$(param /mpd-api/alarm-emails/staging)" \
     -c alarmEmailsProduction="$(param /mpd-api/alarm-emails/production)"
   ```

   **Then give `ci.yml` the role**, as a repository secret (a variable would print the account id in the public logs), and record a result for the commit the pipeline is releasing:

   ```bash
   gh secret set SENDREC_CI_RECORD_ROLE_ARN --repo My-Performance-Doctor/sendrec \
     --body "arn:aws:iam::$DEPLOYMENT_ACCOUNT:role/sendrec-github-ci-record"
   gh run list --repo My-Performance-Doctor/sendrec --workflow ci.yml --branch main --event push --commit <the commit the pipeline is releasing>
   gh run rerun <that run's id> --repo My-Performance-Doctor/sendrec
   ```

   The rerun's `record-result` job should end "Recorded success for <commit>". Check the item with `aws dynamodb get-item --table-name sendrec-ci-results --key '{"commit":{"S":"<commit>"}}' --profile <deployment-admin>`.

6. **Read the staging diff before the first staging deploy.** The pipeline starts a run on creation, so disable the transition into `Staging` at once, then diff with staging read credentials and the same variables:

   ```bash
   aws codepipeline disable-stage-transition --pipeline-name SendRec --stage-name Staging --transition-type Inbound \
     --reason "First run: read the staging diff" --region ap-southeast-2 --profile <deployment-admin>
   cdk diff 'SendRecPipeline/Staging/SendRecStaging' --profile <staging-read> -c targetEnvironment=pipeline \
     -c releaseRevision=<run's commit> -c releaseDigest=<run's digest>
   ```

   Expect only the two task definitions' image, `MigrationRun` with its Lambda, role and policy, and the service's new dependency; no `MPD_*`, switch or transcription change. Then enable the transition (`enable-stage-transition`), watch staging deploy, and check `/api/health` and `/api/ready`.
7. **Read the production diff before approving.** With production read credentials and the same variables, `cdk diff 'SendRecPipeline/Production/SendRecProduction' -c targetEnvironment=pipeline -c releaseRevision=<run's commit> -c releaseDigest=<run's digest> …`. Expect only the two task definitions' image, the `MigrationRun` resource, its Lambda, role and policy, and the service's new dependency; `DesiredCount` must stay at production's count. The release also ships every change on `main` since production's last release, so check `DEPLOYMENT.md`'s release gate for migrations first.
8. **Approve the first production deploy by hand**, during the window and under the grant. Watch `PromoteImage`, the deploy (the migration runs first) and `VerifyProduction`; check `/api/ready` three times and the logs for errors.
9. **Set the hold to `off`.** The next 17:20 release runs on its own.

To back out before the first production approval, delete the `SendRecPipeline` stack. Both SendRec stacks keep what they last deployed, and the hand release in `docs/agents/claude/production-release.md` still works. After the first production release through the pipeline, that runbook is the fallback only.
