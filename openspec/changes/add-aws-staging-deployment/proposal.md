## Why

MPD needs to evaluate a self-hosted replacement for Vimeo before changing the patient video pipeline. A SendRec deployment in the staging AWS account lets staff test recording, sharing, captions and webhooks with synthetic videos.

## What Changes

- Keep the upstream fork at `My-Performance-Doctor/sendrec` and deploy from the local checkout using Java CDK.
- Add an isolated SendRec database, private recordings bucket, Fargate service and private load balancer behind AWS-managed CloudFront HTTPS in the existing staging VPC in `ap-southeast-2`.
- Use task-role credentials for S3 and Secrets Manager injection for database credentials and the session signing key.
- Prepare local Whisper transcription inside the task, with no external transcription service.
- Disable public registration by default. Limit owner bootstrap access before enabling registration, then disable registration again before opening access.
- Verify upload, playback, password protection, captions and signed webhooks using synthetic fixtures.
- Document DNS-free deployment, teardown and retained resources.

## Capabilities

### New Capabilities

- `aws-staging-deployment`: A reproducible SendRec evaluation environment with HTTPS, private data storage, task-role access and checked deployment outcomes.

### Modified Capabilities

None. No OpenSpec capabilities existed in this fork before this change.

## Impact

The scope includes `infrastructure/`, `internal/storage/storage.go`, its credential tests, `docker-entrypoint.sh`, entrypoint tests, `.dockerignore`, repository operating instructions, the infrastructure validation workflow and the inherited preview workflow guard and this OpenSpec change. Application and deployment dependencies remain Go, the existing web tooling and Java CDK.

AWS account `537421187871` will gain dedicated SendRec resources. Existing MPD services, patient data, Vimeo reports and `mpd-api` will remain outside this change. External DNS is deferred at the operator's request. CloudFront provides a trusted AWS-managed hostname without it. No production rollout or integration is included.

The operator merged proposal PR #1 and authorized apply. During implementation, the operator requested an alternative to external DNS and authorized this machine for synthetic owner setup. The design below records that steering within the same change.
