# Readiness evidence, 30 October 2026

This is the MPD-211 gate 3 handoff for `prepare-production-video-service`. It records completed checks and remaining work. Production activation still requires Josh's go, a maintenance grant and a separate rollout decision. Nothing in this record closes those gates.

## Verified work

| Check | Evidence and limit |
| --- | --- |
| Release controls | [PR 16](https://github.com/My-Performance-Doctor/sendrec/pull/16) and [PR 17](https://github.com/My-Performance-Doctor/sendrec/pull/17) merged after CI and three recorded review rounds each. They add operational evidence, recovery notices, database headroom alarms and promotion of the same digest across accounts |
| Failed candidate | The 26 October staging rehearsal restored definition 12 after readiness failed, retained the forward migration and verified the prior application. The single-task staging service was unavailable for about ten minutes. This did not demonstrate uninterrupted production rollback |
| Two tasks | The 26 October probe saw no 5xx while one of two tasks was stopped and replaced. No media work ran during that drill |
| Alarm delivery | The 26 October staging fault reached an SNS/SQS subscriber as ALARM after 89 seconds and as OK 104 seconds after clearing. Production email delivery remains unchecked |
| RDS restoration | The 26 October isolated point-in-time restore took 31 minutes to verify. Counts, a fingerprint and referenced-object existence matched. The application did not run against the restored database; staging cannot test S3 version restoration because its bucket is unversioned |
| Protected media and identity | The 26 October synthetic staff-session checks are recorded in [verification.md](../mpd/verification.md). They cover the report, password denials with an authorized control, playback, captions, preview, MPD sign-in, renewal and recovery cases |
| Real camera and Safari | HQ reported on 30 October that Sami completed the seven-step real-camera and Safari pass and marked MPD-210 Done. Browser/OS versions and per-case results were not supplied to this worker. Treat this as operator-reported evidence, not an independently repeated full browser matrix |

The detailed drill timings and limits are in [DEPLOYMENT.md](../../infrastructure/DEPLOYMENT.md). Older unchecked tasks remain open where their whole acceptance condition is not yet demonstrated.

## Work staging still allows

- Run simultaneous synthetic media jobs on two tasks, interrupt a leader and verify one result and one delivered event per operation.
- Run the application against an isolated restored database, with controlled event delivery, and check protected playback, captions, identity and pending-event recovery.
- Complete Firefox and the missing versioned Chrome/Safari recording, upload, protected watch/embed, denied-device and interrupted-network cases. The server browser's synthetic media does not establish real-device capture.
- Exercise deployed staff offboarding and service-token rotation with synthetic accounts and verify the bounded access denial.
- Archive the completed staging change in a separate change of work. No failed review round should be recorded as clean.

## Work requiring a production window

Window A may prepare the alert topic and confirmed email subscription, the immutable image copy and the empty production stack after Josh's go and the maintenance grant. Starting two application tasks also requires passing the outstanding simultaneous-media and interrupted-leader drill on staging, as design decision 2 and task 2.3 require. The earlier HTTP failover probe does not meet that prerequisite. Keep the production service at zero tasks until those results and the rollout approval are recorded.

After that prerequisite passes, run the migration and promote the exact staging-verified digest to two tasks with staff integration disabled. Check the diff before deployment, verify dependency readiness, task replacement, backups and both fault and recovery email receipt. The private grant-window sheet holds account references, exact commands and the requested three-hour window; its two-task step has the same prerequisite.

Window B follows the production callback hostname and Jefferson's mpd-api change. Create the confidential client and integration secrets through the approved workflow, activate both services and run one synthetic end-to-end check. Production secret changes and activation need their own approved grant. No production command ran in this session.

Sami's approved defaults are the operations automation mailbox, db.t4g.small Multi-AZ, a fifteen-minute database RPO and a four-hour RTO. The confidential production client follows the empty stack. Resource creation and measured recovery remain distinct from those approvals.

Keep Vimeo as the fallback. The [migration plan](vimeo-migration.md) requires inventory, matching and verified import before switching links. No patient video has been downloaded for this work.
