# SendRec staging

This stack targets account `537421187871` in `ap-southeast-2` using the existing staging VPC. CloudFront supplies a trusted `.cloudfront.net` HTTPS hostname. Its VPC origin reaches an internal ALB in isolated subnets. Fargate runs in private subnets with egress, and the dedicated encrypted PostgreSQL database runs in isolated subnets. S3 recordings remain private. Application responses use the managed CachingDisabled policy.

The operator approved synthetic evaluation and requested an alternative to external DNS. This deployment does not use the pending regional certificate or require a domain CNAME. A future custom CloudFront hostname needs a separate change and an ACM certificate in `us-east-1`.

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

## Verify and recover

Use synthetic speech and video to check direct signed S3 upload, unsigned-object denial, playback, password denial with a successful authorized control, nonempty local VTT and webhook signature acceptance with tamper rejection. Do not configure a receiver that sends real messages. Replace the service task and verify the video, database entry and transcript persist. Record the exact revision, outputs, template hash and results in `DEPLOYMENT.md`. A runbook is not deployment evidence.

RDS takes daily backups retained for seven days. One Fargate task runs at a time, so a deployment or replacement interrupts service briefly. The model is pinned and checksum-verified at task startup. Concurrent ffmpeg encodes are limited to one. The small Whisper model is an evaluation choice, not approval of clinical transcription accuracy.

The transcription worker requeues an abandoned `processing` job after its 30-minute deadline plus one minute. To recover sooner, use the owner's retranscribe action, backed by authenticated `POST /api/videos/<video-id>/retranscribe`. A successful retry returns 202. Verify the job reaches `ready` and its signed VTT contains cues before calling recovery complete. The staging task-replacement check uses this explicit retry; it does not claim to have waited for the automatic stale-job path.

To recover a bad application update, check out the last verified revision and deploy it with the current approved access contexts. Read the CloudFormation diff first. Do not reset a dirty checkout or replace retained state. A failed initial deployment needs resource and failure readback before any cleanup.

## Retained resources

Stack deletion retains the recordings bucket, database instance and all Secrets Manager secrets. They remain billable until an operator explicitly removes them. The interrupted HTTP draft created an unencrypted `sendrec-staging-db`. The HTTPS update uses `sendrec-staging-encrypted-db`, retaining the old database instead of deleting unknown state. Record both resources in the deployment handoff and leave cleanup to an operator. The controlled Lambda receiver verifies synthetic webhook HMAC signatures and returns 403 for tampering. It never forwards messages or logs payloads. Store the application-generated synthetic webhook secret in its dedicated Secrets Manager secret before testing. An IAM Lambda invocation can also probe the restricted application from an outside source IP.

The ECS service, private ALB, CloudFront distribution, viewer function, synthetic receiver and log group follow the stack lifecycle. The existing VPC and CDK asset registry are shared and must remain untouched.

Before any operator-run teardown, list the stack resources, verify backups and export the synthetic evaluation record. Deleting the stack does not delete retained recordings or the database. Do not automate their deletion or delete the shared bootstrap registry.
