## Context

See `proposal.md` for the purpose and scope. The fork is checked out at `/home/mac-home-lab/Projects/mpd/sendrec` on `feat/add-aws-staging-deployment`, based on upstream commit `8f2a20e`. The branch was renamed from `feat/aws-staging-infrastructure` to bind this change. The interrupted run left staged changes and a successful Docker build, but no SendRec CloudFormation stack. The Java draft compiles; it has no infrastructure tests yet.

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

1. Use Java CDK in `infrastructure/`, Fargate, a dedicated RDS PostgreSQL instance and a private S3 bucket. This follows MPD's existing deployment pattern and avoids sharing an application database or running database storage in an ephemeral task.
2. Use the Fargate task role for S3. Preserve explicitly configured static credentials for upstream Compose users, but use the AWS default credential chain when both are absent. Test both modes, including temporary session credentials. Reject partial explicit credentials with an actionable error.
3. Inject database fields and JWT signing material from Secrets Manager through ECS. Compose a correctly escaped database connection string inside the container. Generated passwords must exclude every URI-reserved character unless the entrypoint percent-encodes them. Do not print connection strings or secret values.
4. Require HTTPS for application traffic and block deployment until the certificate is issued. There is no pre-issuance application or maintenance deployment. Port 80 redirects to the configured HTTPS hostname. This replaces the draft's HTTP application listener, which also cannot support browser screen recording on an ordinary remote origin.
5. Keep registration disabled by default. Bootstrap an owner only while load-balancer access is restricted to an explicit operator CIDR. Remove bootstrap registration before allowing wider access. An email delivery allowlist does not restrict account creation, so it cannot replace this control.
6. Run Whisper locally with the small model on one 2-vCPU, 4-GiB task initially. Validate model availability and handle download failures explicitly. Use a pinned model artifact and verify its checksum; a mutable download URL is insufficient. Audio processing stays inside the AWS account. The model download transfers model bytes only.
7. Deploy directly with the `mpd-staging` profile after proposal approval. A deployment pipeline is deferred. The initial authorized action is a staging evaluation, with no production resources or live sending integrations.
8. Retain recordings, database state and secrets on stack deletion. Record exactly what remains billable and provide operator-run cleanup instructions. Do not delete those resources automatically.
9. Accept the certificate ARN as an explicit non-secret CDK context input, `certificateArn`, rather than baking an existing certificate into the application revision. Validate its account, region, hostname and issued status before deployment. Record the ARN and synthesized template with the code revision. A replacement certificate changes this deployment input without changing reviewed code.

## Risks / Trade-offs

- Whisper and ffmpeg share memory with the HTTP process. Limit concurrent encodes and measure a representative synthetic recording; reduce workload or increase task memory if the process fails.
- A small model may miss clinical vocabulary. Check the transcript against known synthetic speech and report accuracy limits. This evaluation does not approve patient use.
- DNS and owner bootstrap need operator action. Prepare the exact record and restricted bootstrap instructions; do not substitute an insecure public origin.
- Upstream code changes must stay small. Add focused credential and entrypoint tests and preserve the Compose path.
- The single task may interrupt processing on redeploy. Check processing recovery using a synthetic fixture before reporting deployment complete.
- The current implementation draft is unapproved. Preserve it as draft evidence and reconcile it against this design after approval.

## Migration Plan

1. Obtain proposal approval, bind this change and validate it strictly before implementation.
2. Fix the draft's credentials, connection string, model initialization, HTTP exposure and registration defaults. Add focused tests and infrastructure assertions.
3. Compile, test, build the Docker image and synthesize the stack. Check the account, region, resource list, IAM permissions and retention policy in the resulting template.
4. Commit by completed task section, open a PR and run the required review loop. A human merges the PR.
5. Re-read the candidate certificate's status and validation record from ACM immediately before DNS setup. If validation has timed out, failed or the certificate no longer exists, request a replacement for the same hostname. Add the current record through the authoritative DNS administration route. The following record is the 2026-10-07 readback, not a substitute for that fresh check:

   Name: `_f707ede91eaeefded06de516e758ce71.staging-video.myperformancedoctor.com.`

   Value: `_bd0c303c43067bf417b1129809aff5cc.wzccmgtwzk.acm-validations.aws.`

6. Wait for ACM to report `ISSUED` for the expected hostname before creating the stack. Supply that ARN through the reviewed `certificateArn` context input, synthesize again and inspect the deployment diff. Deploy the approved code revision under restricted access. Verify trusted HTTPS using the configured hostname as the TLS server name and the actual ALB as the connection target before publishing the application CNAME to that ALB. Then verify normal DNS access. Bootstrap the owner under restricted access, then disable registration and verify that anonymous registration fails.
7. Verify TLS, load-balancer target health, database migrations, S3 upload and private-object denial. Pair each denial with an authorized positive control.
8. Use synthetic speech and video to check playback, password enforcement, captions and webhook signing. Configure only a synthetic webhook receiver that cannot send patient messages. Never log secret-bearing signed URLs.
9. Record resource outputs, revision, checks and remaining limits. Revert to the prior task revision if an update fails. For an initial failed deployment, preserve state and report the failed resources before considering cleanup.
