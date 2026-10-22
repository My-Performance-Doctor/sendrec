# Verified SendRec staging deployment

Verified on 2026-10-07 through 15:21 UTC using synthetic identities, speech and videos. Live application: https://d2oopdll9i8yvf.cloudfront.net. Account `537421187871`, region `ap-southeast-2`, stack `SendRecStaging`.

The operator approved the proposal, authorized apply, requested an alternative to external DNS and authorized this machine for synthetic owner setup. CloudFront supplies its managed HTTPS hostname through a private VPC origin. The pending custom-domain certificate is unused and does not block this deployment.

## Deployed revision and state

- Deployment source: `9ffd8e7935dedb9c25eaba1a159fc9015933e5bf` on `feat/add-aws-staging-deployment-apply`. Later handoff changes are documentation only. [Implementation PR #2](https://github.com/My-Performance-Doctor/sendrec/pull/2) remains for human review and merge.
- CloudFormation reached `UPDATE_COMPLETE` at 15:15:28 UTC. The live template equals the synthesized normal-access template. Its SHA256, using JSON with sorted keys and compact separators, is `3010142bcefd7ffb5447403266bacf563247cea02b7e1c289ecfcc28e74d89d5`.
- CloudFront distribution `E1SOB3QVORZR22` and its VPC origin are deployed. The managed certificate verifies successfully. HTTP redirects to HTTPS. The internal ALB has a healthy target; its security group permits only the CloudFront managed prefix list on port 80.
- `sendrec-staging-encrypted-db` is available on PostgreSQL 18.6, encrypted and private, with seven-day backups. Pre-upgrade and post-upgrade automated snapshots are available.
- One task runs in private subnets in `sendrec-staging-cluster`, service `sendrec-staging-service`, task definition `sendrec-staging:3`. Registration is disabled. The setup IP restriction has been removed after registration-denial and owner-login checks.
- Running image digest: `sha256:c1ca392553879bc59c5e573d5a3652bd929543bbaedc66c220a715d9108cac58`. Docker asset: `40da559bd02e2aa63fc29195807eddcd7afe496a505fd0b557987f08b7969c00`.
- Recordings bucket: `mpd-video-recordings-staging`, with AES256 encryption and all public access blocks enabled. Signed uploads include temporary task-role session credentials; no static S3 keys are injected.
- Local Whisper uses the pinned small model at upstream revision `5359861c739e955e79d9a303bcbc70fb988958b1`, SHA256 `1be3a9b2063867b937e64e2ec7483364a79917e157fa98c5d94b5c1fffea987b`. Model verification and migrations completed at task startup.

## Verified runtime checks

| Check | Observed result |
| --- | --- |
| Trusted HTTPS and HTTP redirect | HTTPS health 200 with certificate verification 0; HTTP 301 to HTTPS |
| Restricted owner setup | Owner registration 201 and login 200 from the allowed machine; an actual HTTPS request from the AWS Lambda probe received 403 |
| Registration shutdown and wider access | Registration was disabled while the restriction stayed in place. After opening access, outside-IP health was 200, owner login was 200 and anonymous registration was 403 |
| Private direct upload | Signed S3 PUT 200; unsigned GET for the same object 403 |
| Upload CORS | Exact staging origin accepted with 200; an unrelated synthetic origin denied with 403 |
| Password protection | Missing and incorrect passwords denied with 403; correct password accepted with 200 and protected playback returned 200 |
| Playable video | Signed range request 206; full download 200, SHA256 equal to the uploaded H.264/AAC fixture, and ffmpeg decoded it without errors |
| Local captions | Transcript reached `ready`; signed VTT returned 200 with nonempty cues containing the synthetic test speech |
| Signed webhooks | Application test and signed receiver control returned 204; tampering returned 403. Delivery history confirmed signed `video.ready`, `video.viewed` and `webhook.test` events |
| State after task replacement | The first video's database entry, password protection, playback and VTT remained accessible |
| Interrupted processing recovery | The second fixture was `processing` when the task stopped. The replacement retained it; authenticated retranscription returned 202, then produced playable video, a nonempty VTT and a thumbnail, each returning 200 |

The controlled receiver is `sendrec-staging-synthetic-receiver`, at https://egqfuxczhhc5a256qpmvws23na0gsosf.lambda-url.ap-southeast-2.on.aws/. It verifies HMAC signatures, logs only a verification flag and never forwards payloads.

The replacement test stopped task `ed3b66cb9105414cb40c97a4a4c532bc` while processing and verified replacement `5f576c1fb9d2493bad7cb2e1ce4287ba`. The interrupted job still showed `processing` after replacement. Recovery used the owner's explicit retry action. The worker's automatic stale-job requeue waits 31 minutes; that delayed path was not exercised.

## Failures corrected and remaining limits

The first RDS configuration used PostgreSQL 17.9. Migrations and health checks passed, but transcript and thumbnail publication failed with `missing FROM-clause entry for table "old"`. SendRec uses PostgreSQL 18 `RETURNING old/new` syntax. An inspected in-place upgrade to 18.6, followed by a transcript retry and a fresh upload, passed both publication checks. [PostgreSQL documents this syntax](https://www.postgresql.org/docs/18/dml-returning.html).

The database upgrade and deliberate task replacement caused temporary 503 responses. Normal health, target health and owner login recovered. This single-task staging service does not provide continuous availability during replacement.

Browser automation could not start because the host's AppArmor policy blocks the T3 browser sandbox. Browser recording, visual behavior and mobile behavior remain untested. The approved upload alternative passed through live API checks and actual media decoding. No host restraint was changed. Synthetic captions do not establish clinical transcription accuracy.

The full Go suite passed against synthetic PostgreSQL 18, the frontend and Docker builds passed, nine Python tests passed, edge access controls passed and three Java infrastructure tests passed. CI for `9ffd8e7` passed application tests, Docker build and infrastructure validation. Upstream Coolify preview jobs are intentionally skipped in this AWS fork.

Two review rounds are recorded in PR #2: one high finding fixed, then clean. A later clean brief targeted `86358f7` after the head moved, so it was not recorded against a different head. Review of `9ffd8e7` failed with exit 5 because Claude Code reported its session limit. No clean final round is claimed. Strict OpenSpec validation passes; final review and archive remain pending.

## Access and retained resources

The synthetic owner credentials are in Secrets Manager at `sendrec-staging/synthetic-owner`. Retrieve them through an authorized private AWS interface. Do not put them in chat, command arguments or the repository. Email delivery is unconfigured; notification mode is off except for the controlled webhook receiver.

Stack deletion retains the encrypted database, recordings bucket and stack-owned secrets `sendrec-staging/rds-credentials`, `sendrec-staging/jwt-secret` and `sendrec-staging/synthetic-webhook-verifier`. The owner secret was created separately and also survives stack deletion. Retained resources remain billable until an operator removes them.

The interrupted draft's unencrypted `sendrec-staging-db` is still available and retained outside the replacement stack. Its public ALB was replaced by the private origin. The old database was not deleted or inspected for application content. An operator must inspect its metadata and confirm disposal before cleanup. Leave the shared VPC and CDK registry untouched.

## Update, 2026-10-22

- Deployment source: `6219925` on `feat/staff-os-staging-access`, deployed at 04:48 UTC with the normal-access contexts and the MPD integration activated. The service runs task definition `sendrec-staging:9`, image digest `sha256:feba15d199ac1efc692ac06a69b51848a50e4711789703ffd4b8bd68e506d203`. Against the previous template, CloudFormation changed only the recordings bucket's CORS origins, `MPD_ALLOWED_ORIGINS` and the image.
- The image is built with Go 1.26.10 on MPD's own base, `537421187871.dkr.ecr.ap-southeast-2.amazonaws.com/sendrec-base@sha256:1873a2db2afa5a76aa30699c149ed6675480fd7724fae2b18cd694b5eb105a20`: Alpine 3.21.9 with `ca-certificates-20261020-r0`. The deployed image's CA bundle has 118 roots and neither `emSign ECC Root CA - G3` nor `AAA Certificate Services`. The binary reports `go1.26.10`.
- An earlier deploy that day, definition 8 from `a90bb9c`, used a base whose whisper.cpp was tuned to the build host and held AVX-512 instructions, which a Fargate host without them would crash on. Definition 9's base builds with `GGML_NATIVE=OFF`; its `whisper-cli` has no AVX-512 instructions. The first base digest, `a390707441f2...`, stays in ECR unused.
- The replacement tasks applied migrations against the retained database, verified the pinned model and reported zero processing failures, upload failures and stuck jobs.
- Before this, staging had returned 503 since 2026-10-20 04:50 UTC, with the service at zero tasks and the database stopped. Both were restored first.
- `MPD_ALLOWED_ORIGINS` is exactly `https://staff.example.test,https://app-staging.mpd.health`, read back from task definition 9. Pass both origins on every later staging deploy, or CloudFormation removes the missing one from the service and from the recordings bucket's CORS rule.
- After review, `sendrec-base` got the repository policy in `sendrec-base-repository-policy.json`. Before that, a CDK deploy from a machine without the base cached would have failed, because cdk-assets pulls it as the CDK image-publishing role, which could read only the CDK asset repository. A pull of the pinned digest as that role then succeeded. The `Dockerfile` built locally against that digest, and its new check, which also blocks `emSign Root CA - C1` and `emSign ECC Root CA - C3`, found 118 distinct roots and none blocked. Staging was not redeployed.

## Exact remaining action

A human merged PR #2 on 2026-10-07, so its review loop can no longer run; the merge settles the final review. Task 4.4 is complete. Archive `add-aws-staging-deployment` with `openspec archive add-aws-staging-deployment` in its own change of work. No production rollout, patient integration, real recordings or Vimeo migration was performed.
