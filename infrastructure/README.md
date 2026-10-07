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
cdk synth SendRecStaging
```

Run the Go suite with the toolchain in `go.mod`, ffmpeg and a dedicated synthetic PostgreSQL database through `TEST_DATABASE_URL`. Build `web/dist` first. Never point tests at patient databases. CDK builds the repository Dockerfile and pushes the asset to its existing staging bootstrap registry.

## First owner

Verify the source machine's public IPv4 address and use only that `/32`. Bootstrap mode without a valid address fails before synthesis. Substitute that non-secret CIDR in the commands below:

```bash
cdk synth SendRecStaging -c bootstrap=true -c bootstrapCidr=<setup-ip>/32
cdk diff SendRecStaging -c bootstrap=true -c bootstrapCidr=<setup-ip>/32
cdk deploy SendRecStaging -c bootstrap=true -c bootstrapCidr=<setup-ip>/32 --require-approval never --outputs-file /tmp/sendrec-outputs.json
```

Check the deployed viewer function with allowed and denied IP events. Verify HTTPS and `/api/health`, then register only a synthetic owner through the managed hostname. Passwords, session tokens and signed URLs must stay in private local files or Secrets Manager. Never pass them in shell arguments, print them or commit them. Email delivery is unconfigured during evaluation.

Close registration while retaining the source restriction:

```bash
cdk deploy SendRecStaging -c bootstrap=false -c bootstrapCidr=<setup-ip>/32 --require-approval never
```

Wait for the replacement ECS task to become healthy. Verify anonymous registration fails and the existing owner can log in. Only then remove the source restriction:

```bash
cdk deploy SendRecStaging -c bootstrap=false --require-approval never
```

Do not combine those two deployments. CloudFront updates can finish before an ECS task replacement, so changing both at once could expose registration.

## Verify and recover

Use synthetic speech and video to check direct signed S3 upload, unsigned-object denial, playback, password denial with a successful authorized control, nonempty local VTT and webhook signature acceptance with tamper rejection. Do not configure a receiver that sends real messages. Replace the service task and verify the video, database entry and transcript persist. Record the exact revision, outputs, template hash and results in `DEPLOYMENT.md`. A runbook is not deployment evidence.

RDS takes daily backups retained for seven days. One Fargate task runs at a time, so a deployment or replacement interrupts service briefly. The model is pinned and checksum-verified at task startup. Concurrent ffmpeg encodes are limited to one. The small Whisper model is an evaluation choice, not approval of clinical transcription accuracy.

To recover a bad application update, check out the last verified revision and deploy it with the current approved access contexts. Read the CloudFormation diff first. Do not reset a dirty checkout or replace retained state. A failed initial deployment needs resource and failure readback before any cleanup.

## Retained resources

Stack deletion retains the recordings bucket, database instance and both Secrets Manager secrets. They remain billable until an operator explicitly removes them. The ECS service, private ALB, CloudFront distribution, viewer function and log group follow the stack lifecycle. The existing VPC and CDK asset registry are shared and must remain untouched.

Before any operator-run teardown, list the stack resources, verify backups and export the synthetic evaluation record. Deleting the stack does not delete retained recordings or the database. Do not automate their deletion or delete the shared bootstrap registry.
