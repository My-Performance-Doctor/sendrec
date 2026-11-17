# Verified SendRec staging deployment

Verified on 2026-10-07 through 15:21 UTC using synthetic identities, speech and videos. Live application: https://d2oopdll9i8yvf.cloudfront.net. Account `537421187871`, region `ap-southeast-2`, stack `SendRecStaging`.

The operator approved the proposal, authorized apply, requested an alternative to external DNS and authorized this machine for synthetic owner setup. CloudFront supplies its managed HTTPS hostname through a private VPC origin. The pending custom-domain certificate is unused and does not block this deployment.


## Release gate: migrations before application tasks, and an explicit production task count

Staging and production run with `MIGRATIONS_MODE=skip`, so a release applies migrations only through its controlled migration task. Production runs `cef3b00` (17 Nov 2026, MPD-510). The next release of main adds these migrations, which must run in this order, in staging and then in production, before any application task is replaced. Staging ran them migration-first on 17 Nov 2026: 000079 to 000081 exited 0 at 12:20:34 UTC (release of `420ed984`), and the `f6796f72` release's run at 13:16:34 UTC found them current. Production has not yet:

| Migration | From | Adds |
|---|---|---|
| 000079 | #38, `add-mpd-processing-progress` (MPD-498) | `videos.conversion_percent` |
| 000080 | #32, `report-playback-completion` (MPD-460) | playback progress for the 80% event |
| 000081 | #31, `show-staff-profile-on-watch-pages` | `mpd_staff_photos` |

1. Deploy a change that moves only the migration task definition to the release image, with the application task definition unchanged, then run that migration task definition once with `aws ecs run-task` (`promote-release.py` cannot run against this intermediate stack, because the application definition still names the old image). Confirm it exits 0 and reports migration 81 before the application tasks are replaced.
2. Only then deploy the full template, which replaces the application tasks, and then run `promote-release.py`, whose migration rerun is a no-op. Promote's rollback target is the application definition live when it starts, which is already the new one, so rolling back the release means redeploying the previous template at the same task count.
3. Processing progress (`MPD_PROCESSING_PROGRESS_ENABLED`) is the deploy setting `mpdProcessingProgress`. Every deploy with the MPD integration active must pass it, `true` or `false`; leaving it out fails synthesis. Pass the value the live application task definition carries, read with `describe-task-definition`, on every step that should not switch it, including the migration-only step. That step's assembly is still built by hand from the live template with only the migration task definition replaced, because a synth with the new image changes both definitions. Switch it in a separate step after the migrations and the release are in: an environment turns it on only once the mpd-api it calls runs #559 (`add-video-processing-progress`), staging on staging mpd-api, production on production mpd-api. For each switch: synthesize with the new value; the diff should show only that variable in the application task definition and the service's `TaskDefinition` reference that follows it. Deploy, then confirm on a synthetic recording that `GET /api/integrations/mpd/videos/{videoId}` returns a `processing` object with `conversion` and `transcript` steps while the re-encode runs; `conversion.percent` fills in once the encoder reports, and may be null for the first seconds or between attempts. With the setting off the response has no `processing` field. Per-step retry, `POST /api/integrations/mpd/videos/{videoId}/retry` with `step` and `mediaVersion`, is served whenever the integration is active, whatever this setting says: 202 for a failed step at the current media version, 409 `step_not_failed` when the step has not failed or the media version has moved, 422 for a missing or unknown step, a media version below 1 or a malformed body, 404 for an unknown or deleted video, and 503 when a failed transcript is retried with transcription off. Record each switch here with the time; deploy with `false` to turn it off.

Without 000079, conversions still publish. The percent writes and the clear after a publish fail and are logged as warnings, `POST /api/integrations/mpd/videos/{videoId}/retry` answers 404, because its read of the row fails, and with the setting on the metadata read omits `processing`.

Every production synth must pass `productionTaskCount` (1 to 4, normally 2), the count the service should run. 0 is refused: the production stack has run since 13 November, so 0 could only stop it. The template then states that count, so a stack deploy never resets a running service (MPD-502: a template count of 0 would have stopped both production tasks). Staging's template names no count, so a staging deploy keeps whatever count the service runs. Production's live template has carried `DesiredCount: 2` since the 17 November release (MPD-510), which deployed an assembly patched to hold the count.

Proved on staging 17 Nov 2026: at 2 running tasks, a stack update removing the service's DesiredCount (11:39:24 to 11:40:02 UTC) left the service at 2/2; the live template now carries no count. (Run and recorded by the release coordinator for `require-production-task-count`.)

### Processing progress switches

| Environment | State | Revision | Evidence |
|---|---|---|---|
| Staging | on since 17 Nov 2026 15:49:41 UTC (`sendrec-staging:31`) | `f6796f72`; staging mpd-api `957874e1`, which carries mpd-api #559 | A separate step after the `f6796f72` release. Its synth diff changed only `MPD_PROCESSING_PROGRESS_ENABLED`, false to true, in the application task definition, plus the service's `TaskDefinition` reference that follows a new revision. Staff OS on app-staging, synthetic patient, 15:58 to 16:15 UTC: the Outbox bar showed Preparing video 42%, 61%, 72%, 80% and 90%, then Writing summary, then Ready to send; those percents come from SendRec's `processing` field through mpd-api. A direct `GET /api/integrations/mpd/videos/{videoId}` was not run. For the failure check staging ran a temporary task definition, `sendrec-staging:32` (invalid Transcribe locale, no Whisper model, progress on), from 16:05:43 to 16:13:42 UTC, then returned to `:31`; `:32` is still registered, unused. A forced transcript failure showed Retry transcript, and the retry (202) finished the take (MPD-498). |
| Production | not present: `cef3b00` predates #41 and carries no `MPD_PROCESSING_PROGRESS_ENABLED` | `cef3b00` | The first release after #41 passes `false`. Switch on as a separate, recorded step, possibly in the same ProductionMaintenance grant after that release's read-back, once both hold: (1) a release at `f6796f72` or later (it carries migrations 000079 to 000081 and #41's setting; #38 to #40 ignore the setting) is in production, run migration-first per steps 1 and 2; (2) production mpd-api runs mpd-api #559, through its own release. Run step 3's full check, including the direct `GET`. |

## Deployed revision and state

The 7 October 2026 snapshot of the first staging deployment. Later releases are in the dated updates below; the current state is in the release gate and switch table above.

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

## Update, 2026-11-09: staff preview frame

Josh had PR #23 (`fit-mpd-preview-to-frame`) merged as `b3f937ad4a35b3ac2b28ecafe2b1b3bdd7f48e56` and promoted to staging. No review by Sami of its proposal was recorded on the PR; Josh made the merge call. PR #22 waits for that review and is not deployed. No production resource changed.

Main CI on `b3f937a` passed the frontend, lint, Go test and module checks, then failed `govulncheck` on GO-2026-6909. That advisory covers `github.com/crewjam/saml` v0.5.1, has no fixed version yet, and is reached from the SAML ACS callback in `internal/sso/saml.go`. The previous staging image, `3d79c3c8`, carries the same module version, so this release neither adds nor removes that exposure.

- The image was built for linux/amd64 from the merge commit on the pinned base. The CA check found 118 distinct roots, none blocked. It was pushed to `sendrec` tagged with the full revision, digest `sha256:06c58d4ae30e1faef5f2bae61d64b69aac1cfd0a51ec6a1dc9e23038af8ed6a4`.
- Contexts were read back from task definition 15. `MPD_ALLOWED_ORIGINS` stayed exactly `https://staff.example.test,https://app-staging.mpd.health`. With the previous digest as `releaseImage`, synthesis equalled the live template in every resource but `CDKMetadata`. With the new digest, the diff changed only the application and migration image references and the service's task definition.
- The CDK deploy ran from 00:34:41 to 00:38:46 UTC. The service runs `sendrec-staging:16` with rollout `COMPLETED`; the migration definition is `sendrec-staging-migration:4`.
- `promote-release.py` ran for staging from 00:38:59 to 00:39:43 UTC. It checked the immutable repository and the revision tag, the definitions' image and migration modes, one migration task (exit 0), the task count and three readiness probes. The production half of the template pair needs the private grant-sheet inputs, which this host lacked. A wrapper therefore replaced only the pair digest with an exact comparison of the live staging template against the reviewed synthesis, SHA256 `3786cc832e2b831be1b99aa9b4c1b5a6c431082b658a39df70ff3c2f07e4eb7e`.

| Check | Result |
| --- | --- |
| Health and readiness | Health 200 with version `b3f937ad`; database, storage and workers ready |
| Preview styles under the CSP | `/mpd-preview` carries one `<style>` whose nonce equals the CSP `style-src` nonce, with no `style` attributes. `frame-ancestors` still lists `'self'` and the two staging origins only |
| Preview in Chromium at 560 × 315 | Black page, heading visually hidden, status centred, player 560 × 315 with `object-fit: contain` and hidden without media, no scroll. At 1280 × 800 it also fits without scroll. Opened directly, without a handoff, the status read "Access ended…", as it should |
| Regressions | Unknown watch page and watch API 404, `player.js` and the app 200, anonymous registration 403. No error or panic in the service log in the 15 minutes after the deploy |

Not checked: the preview framed by Staff OS with a loaded recording, and Safari and Firefox. Task 2.1 of `fit-mpd-preview-to-frame` stays open for a staff sign-in.

## Update, 2026-11-12: current candidate and readiness drills

The [dated readiness record](../docs/production/readiness-2026-11-12.md) records MPD-187 and MPD-213 evidence and the remaining production gates. No production AWS command ran. The separate GitHub `production` environment was configured under HQ's instruction and read back: `sami-abdul` or `Sunnndayyy` may approve, self-review is prevented, no wait timer applies and deployments are restricted to `main`.

Source `5859b8dcac0ad6771e6f8a3fb2a8033337e045e7` was built with Go 1.26.11 and promoted at immutable index digest `sha256:16528efc5d292ba4b193a53de8105ad54d64ceee8750263d999fb7da126c5680`. After two public module-proxy failures, an external build context supplied cached modules. A separate check with the same compiler reported `all modules verified`; no repository build file changed. Source CI passed.

Migration 77 ran in a controlled candidate-image task before the application replacement. CDK selected application definition 18 and migration definition 6, preserving the exact staging integration configuration. The subsequent controlled promotion ran a serial no-op migration and dependency readiness checks. Its staging-only template-array hash was `f51d9b3b4efe563e195e8484f187e38df7031eebb9c5a6090f04024142e0dd14`. Production promotion still needs the actual reviewed template pair and its matching standard staging receipt. This receipt is not production approval. The single-task replacement produced observed HTTP 502/503 responses from 10:43:27 through 10:45:12 UTC; readiness passed again at 10:45:17. Transport timeouts were recorded separately.

The isolated restored application passed protected playback, decoding, captions, fresh PEM authorization, isolated S3 version recovery and retained-event restart. RDS availability took 30m03s, application readiness 33m40s and all checks 41m56s. It used the prior deployed revision `f45db5c1`, a clone-only password and an event seeded after restoration. Original-password acceptance, a pre-PITR pending event and production recovery guarantees remain unverified. All owned restore resources were removed; source retention and storage settings stayed unchanged.

The application ran on the agent host through an SSM tunnel, not as an ECS service. Its readiness timing excludes ECS replacement and ALB cutover. Cloned pending events were quarantined before the worker started, so redelivery of events acknowledged by the source after the restore point was not exercised.

Chromium 151 and Firefox 153 on Ubuntu 26.04, plus container WebKit 26.5 on Ubuntu 24.04.4, passed the synthetic cases itemized in the dated table. Protected watch/embed, captions and actual 310-second pause/resume passed in all three engines. This does not complete the real-device Safari or full capture matrix.

Fresh offboarding of a dedicated synthetic account denied library access within 12.358 seconds, preview with 403 and a new session exchange with 401. An independent protected-link client retained renewal 200, media 206 and its media version. Shared staff memberships were untouched. Service-token rotation remains open.

Two healthy targets in separate zones claimed simultaneous 90-second MP4/WebM jobs. The first stop drained both initial transcriptions at generation 1. A second pair of intentional retranscriptions established actual abandonment: the task stopped at 11:09:28 UTC with its generation-2 job still processing. The target drain was temporarily zero for that fault and was restored to 30 seconds and read back. The live service reclaimed the row at 11:39:11.938 UTC and completed generation 3 at 11:42:46.517, 33m17.980s after the task stopped. No timestamp was edited and no retry was requested after interruption.

The final read-only snapshot shows exactly two recording rows, two current media keys and two current transcript keys, with media version 1 on both. The interrupted row published generation 3 and the peer generation 2. Six distinct lifecycle event IDs and deduplication keys were each acknowledged after one HTTP-202 delivery attempt; the abandoned generation produced no transcript-ready event. Both current media objects decode at 640 by 360 and their VTTs have 33 and 22 cues. The WebM underwent the expected H.264/AAC MP4 conversion; MP4 source bytes matched exactly. The abrupt-stop monitor completed 335 healthy HTTP-200 requests and recorded 25 transport timeouts. These samples do not establish continuous availability or downstream patient-report effects for these unclaimed synthetic recordings.

At 11:46:31 UTC staging was restored to one running task, zero pending tasks and one healthy target on definition 18, with the 30-second drain preserved and all readiness dependencies true. Both owned synthetic accounts were disabled. Test media remains retained under managed policy; shared memberships and the original fixture were untouched.

The remaining worker drill then ran on the same candidate during a two-task deployment and task replacement. Two healthy targets in separate zones were confirmed after replacement before the video reset and thumbnail recovery. A scoped row lock prevented a real owned edit from publishing before its task stopped at 12:10:29 UTC. Without a timestamp edit or retry, the normal sweep reset the row at 12:24:39.692, 67.364 seconds after its actual eligibility threshold. Original media bytes, protected playback and captions survived under reserved media version 2. During the interruption drill, eligible orphan cleanup returned 404 while referenced live media stayed 200; concurrent cleanup by two tasks was not established. An owed thumbnail regenerated at 12:21:09.198 and decoded at 640 by 360, with exactly one physical thumbnail object and matching content version. Managed user and workspace retention changes returned 403 and stayed at zero; the seeded old warning did not delete or purge media after the task interruption. Six distinct lifecycle events were each acknowledged once.

This closes the measured worker gate with optional sending workers and legacy webhooks disabled. There was no active retention leader; safe concurrent optional retention or sending remains unverified and those workers must stay disabled. Standard production promotion requires two tasks and rollout approval with the tested settings. The dated record keeps empty-stack preparation and later patient activation separate.

After restoring the seeded warning to null, staging returned to one healthy definition-18 task at 12:29:39 UTC, with zero pending tasks, the 30-second drain and all dependency checks true. The synthetic media remains retained; the owned cleanup orphan is gone. No shared account or policy changed.

## Update, 2026-11-16/17 UTC: production capacity release and an Amazon Transcribe staging trial

Superseded on 17 Nov 2026 by the release gate and switch table at the top: production runs `cef3b00` (MPD-510) and staging `f6796f72`. Kept as the record of that day.

The first production recording (16 November, 15:29 UTC) uploaded in 46 seconds. Then Staff OS failed the take at 10 minutes, three normalizes ran at once, and Whisper was killed at the old 30-minute deadline. `speed-up-playback-encodes` (PR #35) covers the encodes.

Production now runs `cc69b681413d309f9ca1557eb199b9a4c19832e2` (PR #33). It is the image staging had run since 16:55 UTC, at index digest `sha256:ecc1c67b28373bba6a08545e1a9a2bf9a021b7faa573e4e2ba853dfb171ff027`, copied into production's immutable repository with `crane copy`, which keeps the digest. The release used a change set built from the live production template. It changed only the two task-definition images, and set `DesiredCount` to the live 2, because the template's 0 would otherwise have been sent with the update. There was no migration. Before execution, the logs showed no encode or transcription for six hours. The rollout ran from 22:48:17 to 22:51:09 UTC. Health reports `cc69b681`, readiness passed three times, and both new tasks started cleanly.

Production's template then said `DesiredCount: 0` while the live service ran 2. `require-production-task-count` (MPD-502) makes `productionTaskCount` a required production synth input, so a release names the count and an update cannot scale the service to zero.

On 17 November MPD's AWS organization attached the AI services opt-out policy `ai-services-opt-out-all` to its root. The effective policy on the staging and production accounts reads `optOut`.

Staging is trialling `add-aws-transcription` from branch `t3/aws-transcribe-provider` at `f761f1f3229c83b5197589e73697306c5eb5e28f`, digest `sha256:4b3cbe2ba06bc9c9ae21a97a5155c8569b9ea26e4e5d36fe16e0b9a896d1daf8`, with `transcriptionProvider=aws`, before review. The change set took from a CDK synth with the live staging contexts only the two task definitions, the task-role policy and the bucket lifecycle. It kept the live `DesiredCount` of 2, which differs from the code's 1. The stack was updated from 23:07:59 to 23:12:03 UTC, and staging runs definition 24. Health reports `f761f1f3`, readiness passed, and both workers started with `aws-transcribe`.

A re-transcription of the synthetic 20-minute recording `5201bf55-f802-4c16-b3f3-9e2564b0f9b1` ran from claim to `transcribe: completed` in 25.6 seconds. It published generation 2 with 222 segments ending at 1199.8 seconds. Afterwards `transcribe/` held only Transcribe's empty `.write_access_check_file.temp`, and no `sendrec-*` job remained. A one-off psql task with the migration execution role queued the job. Its definition is deregistered.

Redeploy merged `main` to staging if this branch changes or is not merged.

### Amazon Transcribe cost and failure handling

The AWS price list (`aws pricing get-products --service-code transcribe`, effective 1 June 2026) gives standard batch transcription in Sydney, usage type `APS2-TranscribeAudio`, as a flat US$0.0001 a second with no volume tiers. That is US$0.36 an hour of audio, about US$0.06 for a 10-minute recording and US$0.12 for a 20-minute one. Billing is per second of audio, so a retry or a job a stopped task abandoned is billed again. Transcribe Medical (`APS2-MedicalTranscribeAudio`) is US$0.00125 a second, US$4.50 an hour, and is not used.

A status check that fails with throttling, a limit, a 5xx or a network error is retried with a longer wait until the job's deadline, and a start is tried three times. Access, validation and missing-job errors go to the fallback at once. If Transcribe cannot finish the job, the same audio is transcribed with local Whisper inside the job's deadline. Transcribe gets ten minutes plus half the recording's length, and never more than half the deadline; Whisper keeps the rest, about 20 minutes plus 3.5 times the length, enough for dense speech on a slow host. Whisper starts at once and yields the CPU to encodes at nice 19. A Transcribe job abandoned while running that completes during its cleanup still wins if it finishes before Whisper. Without the Whisper model, Transcribe gets the whole deadline. The entrypoint downloads the Whisper model for the `aws` provider too; without it the fallback is skipped with a warning. Logs name the provider that produced each transcript.

The task role may start a job only with its output under `transcribe/` in the recordings bucket. Object reads and writes an AWS service makes for the role outside `transcribe/` are denied. Cleanup deletes every version of the staged audio at once. For an abandoned job it waits in the background, up to three minutes, for the late output, deletes it and the job, so a fallback starts at once. A task that is stopping cancels its running transcript, cuts that wait to five seconds and waits up to 12 seconds for the cleanups. The container's stop timeout is 60 seconds, to fit the hand-back, this cleanup, encode lease release and the HTTP drain. Anything left under `transcribe/`, current or noncurrent, expires after a day, and expired delete markers are removed.
