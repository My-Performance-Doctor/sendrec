## 1. Release prerequisites

- [ ] 1.1 Reconcile and close the existing staging change using actual merged-source review and verification evidence; validate and archive it without inventing the missing final review.
  - Reconciled on 2026-10-30: the staging change has all fourteen tasks checked. Its merged PR #2 closes the original review, as `infrastructure/DEPLOYMENT.md` records. Archive remains separate work; do not invent a clean result for the failed final automated review.
- [ ] 1.2 Confirm all four integration changes are human-merged and deployed to staging with matching contracts; verify their revisions, disabled production settings and synthetic evidence.
  - The integration has since run on staging under an MPD staff session. `docs/mpd/verification.md` records the 26 October synthetic checks. Exact revisions for all four dependencies and the remaining acceptance conditions still need reconciliation before checking this whole task.
- [ ] 1.3 Capture the production account/VPC, identity references, operational alert destination, cost estimate and proposed recovery objectives for operator review; verify no live deployment is implied.
  - Operator review held on 2026-10-12 and recorded in `docs/production/operator-decisions.md`. Sami approved the alert destination, db.t4g.small Multi-AZ and fifteen-minute database RPO/four-hour RTO on 26 October, as the private grant-window sheet records. The production Cognito client and alert resources still need creation in approved windows.
  - 2026-10-26: the database class is now the `productionDatabaseSize` context, defaulting to small. The four open inputs and their recommended defaults are in `docs/production/operator-decisions.md`.

## 2. Infrastructure and release controls

- [x] 2.1 Extend Java CDK with isolated production configuration, private encrypted resources and exact origins; verify synthesis and automated infrastructure assertions.
  - Verified locally: six Java assertions pass and synthetic production synthesis shows private encrypted resources, exact CloudFront origins, separate VPC/bucket, zero application tasks and disabled integration flags.
- [ ] 2.2 Add immutable-image promotion, a single migration task, staging checks and a manual production approval; verify failed readiness triggers a safe rollback rehearsal.
  - Implemented digest-bound build/promotion, one migration task and production-environment approval gate. Controlled tests prove failed-readiness rollback without database reversal.
  - 2026-10-26, staging: promotion of `3de745b` from the immutable `sendrec` repository passed, and a candidate that failed readiness was rolled back to the prior definition by the circuit breaker and confirmed by the script, without reversing the migration (`infrastructure/DEPLOYMENT.md`). The script now names the outcome, and a manifest can name production's own copy of the reviewed digest in the production account. Still pending: the GitHub `production` environment with named reviewers, the release roles and workflow variables.
- [ ] 2.3 Configure proposed multi-task and Multi-AZ operation; inventory every startup worker and use atomic claims or single-leader leases, or explicitly disable unsupported optional workers. Test media, stuck recovery, cleanup, retention, digest/onboarding email and legacy webhooks with controlled sinks and interrupted leaders before scaling.
  - Worker controls and disabled optional sends are inventoried in the runbook. Local concurrency/recovery checks exist.
  - 2026-10-26, staging: two tasks across two zones served through a task stop and replacement with no 5xx and no worker errors. No media work ran, so overlapping media jobs, stuck recovery and outbox delivery across two tasks remain pending; they need uploads under a staff session.
- [ ] 2.4 Add health/readiness and backlog/processing/backup alarms with redacted logs; inject synthetic faults and verify the configured operator actually receives alerts.
  - Implemented dependency readiness, numeric probes and nine alarms. Local fault tests pass.
  - 2026-10-26: every alarm now also notifies on recovery, and RDS free-storage and freeable-memory alarms make eleven. On staging, a synthetic fault reached an SNS subscriber as ALARM after 89 seconds and as OK 104 seconds after clearing. Receipt at the production topic's email address remains pending.

## 3. Recovery and client verification

- [ ] 3.1 Configure reviewed production backup/versioning retention without automatic source-media deletion; verify managed user/workspace retention changes are refused and stale nonzero retention_days plus old warnings cannot cause worker deletion or later S3 purge. Keep staging and retained legacy resources untouched.
  - Synthesis verifies 35-day production backups, versioned media with no expiry, and unchanged seven-day staging backups. Application retention guards are implemented; final stale-setting cleanup evidence and reviewed production policy remain pending.
- [ ] 3.2 Restore a synthetic database and media set into isolation; measure RPO/RTO and verify decoding, passwords, captions, identities and pending-event recovery against the proposed objectives.
  - Local isolated PostgreSQL/media restore passed, including two decoded versions, passwords, captions, identity and pending-event claim. Measured 0.191s snapshot age and 1.013s restoration do not establish AWS RPO/RTO.
  - 2026-10-26, staging: an RDS point-in-time restore into an isolated instance took 30 minutes to become available and 31 to verify. Every count and a fingerprint over media keys, passwords and versions matched the source, and all referenced objects exist. Still pending: the application running against a restored database (playback, captions, sign-in, event delivery) and an S3 version restore, which staging's unversioned bucket cannot show.
- [ ] 3.3 Complete the Chrome/Safari/Firefox recording/upload/watch/embed matrix with actual browser/OS versions, denied-device and interrupted-network cases; leave unavailable checks explicitly incomplete.
  - HQ reported on 30 October that Sami's seven-step real-camera and Safari pass worked and MPD-210 is Done. This worker has no browser/OS version or per-case record for that pass. Firefox and the complete versioned matrix remain open. Server-browser synthetic checks are in `docs/mpd/verification.md`; they do not prove real-device capture.
- [ ] 3.4 Exercise the synthetic future-client contract, offboarding, service-token rotation and safe deployment rollback; verify no Staff OS UI or live sending integration is required.
  - Real Go producer/HTTP adapter and Python consumer/DB integration passed locally, including lost-response retry, publication, playback and deletion. ECS rollback ran on staging on 2026-10-26. Actual staff offboarding and service-token rotation across deployed services remain pending.

## 4. Production handoff

- [ ] 4.1 Publish the measured evidence, immutable revisions, configuration diff and operating runbook; verify secret scans and strict OpenSpec validation pass.
  - Runbook and local measurements are recorded. Final immutable revisions, reviewed configuration diff, secret scan and strict validation evidence remain pending at this reconciliation.
- [ ] 4.2 Open the readiness implementation PR and complete review/CI; present the concrete production rollout for explicit operator approval before deployment.
  - Readiness PR #16 and cross-account promotion PR #17 merged on 26 October after green CI and three recorded review rounds each. The 30 October reconciliation is in `docs/production/readiness-2026-10-30.md`. The concrete Window A commands are in the private grant-window sheet. Josh's go and Sami's production grant remain pending, so this whole task is not checked and no production deployment is authorized.
- [ ] 4.3 After separate rollout approval, deploy the empty production service and verify approved pilot checks; record the exact live state and preserve Vimeo service and retained resources.
  - Not authorized or run. Production remains unactivated; no pilot or retained-resource changes occurred.

Delivery note, reconciled 2026-10-30: implementation PR #4 and readiness PRs #16 and #17 are merged. The review records for #16 and #17 contain three rounds each. The 26 October deployed drills and their limits remain in `infrastructure/DEPLOYMENT.md`; the remaining checks are listed in `docs/production/readiness-2026-10-30.md`. Neither merged preparation code nor Sami's successful staging pass authorizes production activation. Window A still waits for Josh's go and a production maintenance grant.
