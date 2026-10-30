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

## Update, 2026-10-22, audit fixes

- Deployment source: `258ebd0` on `main`, the merge of PR #12, which also carries PRs #10 and #11. Deployed at 08:27 to 08:33 UTC with `cdk-with-session.py deploy SendRecStaging`.
- Contexts were read back from task definition `sendrec-staging:9`: `activateMpdIntegration=true`, `bootstrap=false` with no `bootstrapCidr`, `mpdIntegrationSecretArn` naming the existing MPD integration secret, and `MPD_COGNITO_ISSUER`, `MPD_COGNITO_CLIENT_ID`, `MPD_ACCESS_URL`, `MPD_WORKSPACE_ID`, `MPD_TENANT_ID`, `MPD_ALLOWED_CLIENT_IDS`, `MPD_ALLOWED_ORIGINS`, `MPD_EVENT_RECEIVER_URL` and `MPD_EVENT_KEY_ID` with their definition 9 values. `MPD_ALLOWED_ORIGINS` stayed exactly `https://staff.example.test,https://app-staging.mpd.health`.
- The CDK diff showed only the recordings bucket's new lifecycle rule, the container image and the service's task definition.
- The service runs task definition `sendrec-staging:10` with rollout `COMPLETED`, image digest `sha256:7bad70cf165aad2a31ea693c2321e4d399402d5632d47308e73e5c2a3f40b0ff`. The image build's CA check found 118 distinct roots and none blocked.
- Checks after the deploy:
  1. `/api/health` answered 200.
  2. With a password set on a synthetic video, the watch page's title was "Protected video" followed by the workspace name, and the embed page's was "Protected video". Neither page held the recording title.
  3. Ten wrong passwords for that video returned 403. The eleventh returned 429, and the right password then returned 429 with `Retry-After: 900`. Requests were 2.2 seconds apart, under the per-IP limit, so the per-video lock answered. The test password was removed afterwards.
  4. The recordings bucket has lifecycle rule `ExpireOldVersions`: noncurrent versions expire after 30 days and incomplete multipart uploads are aborted after 7. Staging versioning has never been enabled, so the version part takes effect only once it is.
- Production was not deployed.

## Update, 2026-10-26

Main `3de745b`, the merge of PR #15, on staging, then the production release path rehearsed there. All checks used synthetic media. Production was not touched.

### Promotion of main

- Deployed from 02:56 to 03:09 UTC with `cdk-with-session.py deploy SendRecStaging` and the contexts read back from task definition 10. The diff changed only the image and the service's task definition. The service ran `sendrec-staging:11`, image digest `sha256:64158935c65f0455e63e77665ac0d54a8ef434cc46bbd30a2d4d0a1e0ac975a0`; health and readiness answered 200.
- The `MPD immutable candidate` and `MPD approved immutable promotion` workflows could not be used: the repository has no workflow variables or release roles, no `production` environment and, until today, no application ECR repository.
- #15's behaviour: two synthetic recordings whose frame size changes mid-take went through the same image digest, run against a disposable PostgreSQL 18 and Garage stack. One was an avc3 MP4, the format Staff OS #137 records, the other a VP8 WebM, each 640x480 for four seconds and then 1280x544. Both came out at one size, 640x480, at 60 frames a second and the original eight seconds. The top and bottom 40 rows held picture in the first half and black in the second, with the picture in the middle, so the wider half was fitted inside the first frame's size, not squeezed into it. The avc3 upload was re-encoded to avc1. The staging check ran off staging because the synthetic owner in `sendrec-staging/synthetic-owner` now gets 401 from local login, and staging holds no synthetic staff credentials.

### Controlled release on staging

- Created ECR repository `sendrec` with immutable tags and scan on push, and pushed the same digest tagged with the full source revision `3de745bef306212111ac3155ab486fde9d5efc4d`.
- Deployed at 03:49 to 03:55 UTC with `-c releaseImage=537421187871.dkr.ecr.ap-southeast-2.amazonaws.com/sendrec@sha256:64158935…`. The diff added the migration task definition and its roles, moved the image to the `sendrec` repository, set `MPD_MANAGED_MODE=true`, `MIGRATIONS_MODE=skip`, `LEGACY_WEBHOOKS_ENABLED=false`, `OPTIONAL_WORKERS_ENABLED=false`, `AI_ENABLED=false` and `API_DOCS_ENABLED=false`, and moved the target group's health check to `/api/ready`. Staging now runs `sendrec-staging:12` and migration definition `sendrec-staging-migration:1`. Readiness reports database, storage and workers true; anonymous registration returns 403.
- The live template equals the synthesized one. With a production template synthesized from read-only lookups, the release manifest's infrastructure digest was `470dd7e2537b21f90514976874c9e46c3030a689d2911eb6d1d7c9c3ebd51f2c`.

### Release rehearsal

| Check | Result |
| --- | --- |
| Promotion | `promote-release.py --target staging` ran one migration task, which exited 0, kept definition 12 and passed readiness three times. 03:56:30 to 03:58:31 UTC |
| Rollback | A candidate, definition 13, pointed at a bucket that does not exist, so storage readiness failed. ECS's circuit breaker stopped it after three failed tasks and rolled back to definition 12; the script then confirmed definition 12, one running task and readiness, and failed the release. The migration was not reversed. Staging answered 502 or 503 from 04:00:32 to 04:10:15 UTC, because staging runs one task with minimum healthy 0%. Production uses 100% and 200%. Definition 13 is deregistered |
| Failover | Scaled to two tasks in two availability zones, stopped the older one, waited for its replacement, then returned to one task, from 04:11:24 to 04:19:32 UTC. A probe every two seconds saw no 5xx. Its few timeouts matched a control URL's from this host, five in 25 each. Neither task logged an error. No media work ran, so overlapping media jobs on two tasks remain unproven |
| Alarm delivery | A temporary SNS topic with an SQS subscriber, and an alarm on a synthetic metric. ALARM reached the subscriber 89 seconds after the fault was injected; OK arrived 104 seconds after it cleared. The topic, queue and alarm are deleted. Email receipt waits on the production topic and address |
| Restore | Point-in-time restore of `sendrec-staging-encrypted-db` at its latest restorable time, which trailed real time by about four minutes, into private instance `sendrec-staging-restore-drill`. Requested 04:01:18, available 04:31:34, verified 04:32:47 UTC, 31 minutes in all. A one-off task compared both databases: migration 75 clean, 13 ready videos, 7 with link passwords, 13 ready transcripts, 3 identity links, 21 acknowledged events and the same fingerprint over every video's media keys, passwords and versions. All 26 media objects the restored rows reference exist in the bucket. The drill instance and checker are deleted |

The restore did not run the application against the restored database, so protected playback, captions, identity sign-in and event delivery after a restore remain unexercised. Staging's bucket is unversioned, so it could not rehearse restoring object versions. Staging had no writes in the window, so the four-minute figure is RDS's log lag, not measured data loss.

## Exact remaining action

A human merged PR #2 on 2026-10-07, so its review loop can no longer run; the merge settles the final review. Task 4.4 is complete. Archive `add-aws-staging-deployment` with `openspec archive add-aws-staging-deployment` in its own change of work. No production rollout, patient integration, real recordings or Vimeo migration was performed.

## Update, 2026-10-30: transcript time links

Sami merged PR #20 as `3d79c3c8e40445e748bbfb2b6d6583f3c7a88fe7`. Main CI passed. The worker built that revision, tagged the immutable staging ECR image with the full revision and pushed digest `sha256:d2d7a7d2f444c7662d1911bd6e90567cdfec5e19d71fcff547128b522a4d8756`.

The CDK diff changed only the application and migration image references plus the service's task definition. It preserved the live integration settings. The deployment selected `sendrec-staging:15` and `sendrec-staging-migration:3`. The controlled `promote-release.py --target staging` run completed its single migration and verified readiness at 06:47 UTC. The template-pair digest was `674a809b90fe0fea3de17ec6fc8a01d5f3f15ef37e36e0057b2d502326b60c81`; the production half was synthesis only. Health reports version `3d79c3c8`, and dependency readiness reports database, storage and workers true. No production resource changed.

Using an existing synthetic recording from the synthetic patient's record, the worker opened the real staging watch page with `?t=7`. The password gate appeared before playback. After authorized password submission, the video loaded paused at exactly 7 seconds with readyState 4 and duration 12.096333 seconds.

| Keyboard action | Observed media events |
| --- | --- |
| Focus `Play from 0:00`, press Enter | Seeking and play at 0 seconds, seeked at 0.000371 and playing at 0.000683 |
| Focus `Play from 0:07`, press Enter | Seeking, play, seeked and playing at 7.76 seconds, matching the second caption cue's start |

The check verified actual playback from both keyboard actions. It created no recording or upload and invoked no external send. MPD-258 was moved to Done in Plane. The browser tabs and private sign-in relay were closed afterward. This check used Chrome in the server browser; it does not add real-device or Safari evidence.
