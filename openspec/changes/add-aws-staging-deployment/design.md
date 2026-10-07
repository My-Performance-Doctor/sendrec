## Context

See `proposal.md` for the purpose and scope. The fork is checked out at `/home/mac-home-lab/Projects/mpd/sendrec` on `feat/add-aws-staging-deployment`, based on upstream commit `8f2a20e`. The branch was renamed from `feat/aws-staging-infrastructure` to bind this change. The interrupted run left staged changes and a successful Docker build, but no SendRec CloudFormation stack. The interrupted draft is evidence only. Implementation adds focused infrastructure and container tests.

The staging account is `537421187871` in `ap-southeast-2`. The existing VPC has public, private-with-egress and isolated subnets. The staging SSO identity works. ACM certificate `bd6ea8d1-2816-4f54-b812-f5ca8353b11b` is pending validation for `staging-video.myperformancedoctor.com`. No hosted zone is visible in this account. The prior DNS investigation identified GoDaddy; the operator must confirm the DNS administration route.

## Goals / Non-Goals

Goals:

- Keep recordings and database state in dedicated Australian staging resources.
- Preserve the upstream local development path while adding AWS credential support.
- Make success depend on healthy infrastructure and synthetic end-to-end checks.

Non-goals:

- Production deployment, patient data, migration from Vimeo, Google Workspace SSO and changes to `mpd-api`.
- Availability guarantees or separate transcription workers during this evaluation.

## Decisions

1. Use Java CDK in `infrastructure/`, Fargate, a dedicated RDS PostgreSQL 18 instance and a private S3 bucket. SendRec publishes media with PostgreSQL 18 `RETURNING old/new` syntax. Match the upstream CI database version; migrations and health checks alone do not catch an older engine. This follows MPD's existing deployment pattern and avoids sharing an application database or running database storage in an ephemeral task.
2. Use the Fargate task role for S3. Preserve explicitly configured static credentials for upstream Compose users, but use the AWS default credential chain when both are absent. Test both modes, including temporary session credentials. Reject partial explicit credentials with an actionable error.
3. Inject database fields and JWT signing material from Secrets Manager through ECS. Compose a correctly escaped database connection string inside the container. Generated passwords must exclude every URI-reserved character unless the entrypoint percent-encodes them. Do not print connection strings or secret values.
4. Require trusted HTTPS through an AWS-managed CloudFront hostname. The operator explicitly requested a DNS-free alternative on 2026-10-07. Use a CloudFront VPC origin targeting an internal ALB in isolated subnets. The origin has no public route. Disable caching and forward cookies, queries and request bodies for authenticated application traffic. Regional recordings, database and local transcription stay in Sydney. CloudFront processes viewer requests at its global edges. HTTP viewer requests redirect to HTTPS.
5. Keep registration disabled by default. Bootstrap an owner only while CloudFront viewer access is restricted to an explicit IPv4 /32. Disable registration in a first deployment that keeps the restriction. Verify anonymous registration fails and owner login succeeds. Remove the restriction in a second deployment. An email delivery allowlist does not restrict account creation, so it cannot replace this control.
6. Run Whisper locally with the small model on one 2-vCPU, 4-GiB task initially. Validate model availability and handle download failures explicitly. Use a pinned model artifact and verify its checksum; a mutable download URL is insufficient. Audio processing stays inside the AWS account. The model download transfers model bytes only.
7. Deploy directly with the `mpd-staging` profile after proposal approval. A deployment pipeline is deferred. The initial authorized action is a staging evaluation, with no production resources or live sending integrations.
8. Retain recordings, database state and secrets on stack deletion. Record exactly what remains billable and provide operator-run cleanup instructions. Do not delete those resources automatically.
9. Use CloudFront's managed certificate for its generated hostname. Do not attach the pending regional custom-domain certificate. A future custom hostname requires a separately approved change, external DNS and a CloudFront certificate in us-east-1. Keep this evaluation DNS-free.

## Risks / Trade-offs

- Whisper and ffmpeg share memory with the HTTP process. Limit concurrent encodes and measure a representative synthetic recording; reduce workload or increase task memory if the process fails.
- A small model may miss clinical vocabulary. Check the transcript against known synthetic speech and report accuracy limits. This evaluation does not approve patient use.
- The operator authorized synthetic setup from this machine. Verify its public IP immediately before bootstrap. The managed hostname avoids DNS changes; the private origin preserves the HTTPS requirement.
- Upstream code changes must stay small. Add focused credential and entrypoint tests and preserve the Compose path.
- The single task may interrupt processing on redeploy. Check processing recovery using a synthetic fixture before reporting deployment complete.
- Proposal PR #1 is merged. Reconcile the draft against the approved proposal and the explicit DNS-free steering before deployment.

## Migration Plan

1. Obtain proposal approval, bind this change and validate it strictly before implementation.
2. Fix the draft's credentials, connection string, model initialization, HTTP exposure and registration defaults. Add focused tests and infrastructure assertions.
3. Compile, test, build the Docker image and synthesize the stack. Check the account, region, resource list, IAM permissions and retention policy in the resulting template.
4. Commit by completed task section, open a PR and run the required review loop. A human merges the PR.
5. Synthesize and inspect the stack with explicit bootstrap mode and this machine's freshly verified IPv4 /32. Run the implementation PR review loop. Deploy the reviewed branch revision to staging, as authorized by the operator's instruction to complete deployment. Leave PR merging to a human.
6. Verify CloudFront has deployed its managed HTTPS hostname, its private origin is healthy and migrations completed. Test the deployed viewer function with allowed and denied source addresses. Register the synthetic owner from the allowed machine. Disable registration while retaining the address restriction, verify registration denial and login, then remove the restriction.
7. Verify TLS, load-balancer target health, database migrations, S3 upload and private-object denial. Pair each denial with an authorized positive control.
8. Use synthetic speech and video to check playback, password enforcement, captions and webhook signing. Configure only a synthetic webhook receiver that cannot send patient messages. Never log secret-bearing signed URLs.
9. Record resource outputs, revision, checks and remaining limits. Revert to the prior task revision if an update fails. For an initial failed deployment, preserve state and report the failed resources before considering cleanup.
