# Staging readiness, 12 November 2026

This updates [the 30 October record](readiness-2026-10-30.md) for MPD-187 and MPD-213. It records synthetic staging checks, not permission to deploy production. HQ plans Window A for 19 November. It still needs the inclusion decision, Josh's go, a maintenance grant and the gates below. Vimeo remains the fallback.

## Release under verification

- Source: `5859b8dcac0ad6771e6f8a3fb2a8033337e045e7`, the merge of [PR 28](https://github.com/My-Performance-Doctor/sendrec/pull/28). Main application and infrastructure CI passed.
- Immutable candidate: `sha256:16528efc5d292ba4b193a53de8105ad54d64ceee8750263d999fb7da126c5680`. Its revision tag and OCI label identify that source. Binary build information confirms Go 1.26.11.
- Two normal local builds failed with module-proxy HTTP/2 `INTERNAL_ERROR` responses. The successful build supplied the exact cached modules and their public metadata through an external build context. Go downloaded with `GOPROXY=off` and kept its `go.sum` verification. A separate Go 1.26.11 check reported `all modules verified`. The compiler, frontend build, runtime stage, CA-root check and entrypoint match the repository Dockerfile. No repository build file changed.
- The reviewed staging diff changes the application and migration image references and adds `BRAND_FONT_DIR`. It preserves the identity, receiver and origin configuration. The licensed heading font is still absent; Geist remains the fallback.
- Migration 77 ran successfully in a controlled candidate-image task before application replacement. The later promotion check runs its own serial, no-op migration. These are two task invocations, never concurrent migrators.
- CDK selected application definition `sendrec-staging:18` and migration definition `sendrec-staging-migration:6`. The controlled staging promotion passed against the same digest, with staging-only template-array hash `f51d9b3b4efe563e195e8484f187e38df7031eebb9c5a6090f04024142e0dd14`. Health reports `5859b8dc`; database, storage and worker readiness pass. This staging-only hash is not a reviewed production template pair.
- The single-task replacement caused observed 502/503 responses from 10:43:27 through 10:45:12 UTC; readiness passed again at 10:45:17. Transport timeouts in the probe record are separate from HTTP 5xx responses. The staging media checks below passed; the remaining production acceptance gates stay open.

## Two-task media checks

Two healthy staging targets in different availability zones ran the candidate. The initial pair of simultaneous 90-second synthetic uploads was claimed by different tasks. A stop requested while one job was processing allowed both jobs to complete at transcription generation 1 while the task drained. That establishes durable graceful completion, not abandoned-job recovery.

The next case started two intentional retranscriptions of those same synthetic recordings. For this fault only, the staging target group's 30-second deregistration delay was set to zero. A stop was requested at 11:08:19 UTC, shortly after the new claim. The task stopped at 11:09:28 UTC, about 77 seconds after the claim, and its job remained `processing`; a read-only database snapshot confirmed generation 2 with published generation 1. The peer completed generation 2. The original 30-second drain was restored and read back before further checks.

The abandoned claim began at 11:08:11 UTC. The service reclaimed it at 11:39:11.938 UTC, 31 minutes and 0.818 seconds later, and completed it at 11:42:46.517 UTC. Completion took 33 minutes 17.980 seconds from the task stopping, including 3 minutes 34.579 seconds of processing after reclaim. No timestamp was edited and no retry was requested after the interruption.

A final read-only snapshot and real staff preview established:

- Exactly two recording rows, two distinct current media keys and two distinct current transcript keys. Both media versions stayed 1.
- The interrupted row published transcript generation 3; its abandoned generation 2 produced no transcript-ready event. The peer published generation 2.
- Six lifecycle events have six distinct event IDs and deduplication keys: one ready event and two completed transcript generations per recording. Every event was acknowledged after one attempt with HTTP 202. These unclaimed synthetic recordings do not establish downstream patient-report effects.
- Both current media objects decode at 640 by 360. The MP4 retained its exact source bytes and 90.117-second duration. The WebM followed the documented H.264/AAC MP4 conversion, retaining dimensions and a 90.011-second duration against 90.007 seconds at source. Both current VTTs contain cues, 33 and 22 respectively, matching their structured segment counts.
- During the abrupt-stop observation, all 335 completed readiness requests returned 200 with healthy dependencies. Another 25 probes timed out at transport level. This is sampled evidence, not continuous availability.

## Additional worker checks

After PR 29's second review and green CI, the same candidate ran the missing worker checks during a two-task deployment and task replacement. Two healthy targets were confirmed after replacement before the video reset and thumbnail recovery. Only new owned synthetic fixtures were changed. The previous transcription, restore, browser and offboarding drills were not repeated.

An API trim entered processing at 12:08:14 UTC. A transaction locked only that fixture after the claim, preventing publication while the owner task was stopped. The task stopped at 12:10:29 with the row still processing. The transaction then rolled back. The original 30-second target drain was restored. No processing timestamp was edited and no retry was requested. Its real reset threshold was 12:23:32 UTC, the 15-minute bound plus twice its nine-second recorded duration. The normal sweep reset it at 12:24:39.692 UTC, 16 minutes 25.364 seconds after the claim and 67.364 seconds after eligibility. Protected renewal, decoding and three caption cues passed afterward. The original media bytes survived under reserved media version 2, as the failed-edit contract requires.

Cleanup used an owned synthetic orphan and the fixture's referenced live media as a positive control. Both objects returned 200 before their queue entries became due. By 12:15:49 UTC the worker had removed the orphan, returning 404, while live media still returned 200. An intentionally due queue entry tests eligible cleanup; it does not establish that the normal grace interval elapsed. A separate fixture had its thumbnail cleared and version marked owed. After the unchanged ten-minute eligibility period, the normal cleanup loop published its replacement at 12:21:09.198 UTC. The JPEG decodes at 640 by 360, exactly one thumbnail object exists for that fixture, and its thumbnail version matches media version 1 with zero failed attempts.

User and admin-workspace retention changes to 30 days both returned 403; authorized reads before and after confirmed zero days. The thumbnail fixture carried an intentionally seeded eight-day-old warning. It remained ready and unpurged after interruption and replacement, with protected renewal, unchanged media bytes, decoding and three caption cues. Final read-only evidence confirms zero managed retention, no deletion event, six distinct lifecycle event IDs and keys, and one acknowledged attempt per event across the two fixtures. Two healthy targets in separate zones ran definition 18 without environment overrides. Optional sending workers, including retention, and legacy webhooks remained disabled. This configuration has no active retention leader and does not establish safe concurrent operation with optional retention or sending enabled. The gate passes only with those workers disabled.

The initial fault setup rejected the invalid PostgreSQL timeout unit `6 minutes` before touching a row. Its receipt was retained; changing only the setup to `6min` allowed the scoped lock test. No application code or host policy changed.

## Fresh offboarding

A separate dedicated synthetic staff account owned a new protected recording. Creation suppressed the Cognito invitation; the API then bound the existing provider identity without sending mail. Its fresh SRP/MFA login, library read and staff preview all succeeded before the membership and login were disabled through mpd-api.

- Library access changed from 200 to 401 after 12.358 seconds from the disable request, inside the 60-second bound.
- The existing preview returned 403, and a fresh SendRec session exchange returned 401.
- An independent shared-link client was denied without the password before the test. Its authorized renewal stayed 200 after offboarding, media bytes returned 206, and the media version stayed the same.
- The account remains disabled. Shared staging account memberships were not changed. This is staff revocation evidence, not a service-token rotation test.

## Recovery application drill

The source staging database was restored to an isolated RDS instance at its latest restorable point. The selected point trailed the request by 319 seconds. RDS became available after 30 minutes 3 seconds. The restored application first passed dependency readiness after 33 minutes 40 seconds; all application checks finished after 41 minutes 56 seconds.

The application ran the previously deployed revision `f45db5c14c08ec7947ed863e63d5ac20a4958e1e`, image `sha256:e78b894003c80a9606ea71b88128501f2e9b16d8f5a19a794256d0a9a5005bfd`. Its media copy, versioned bucket, event receiver and database access were isolated from the source service.

The application ran on the agent host through an SSM tunnel, not as an ECS service. Its readiness timing excludes ECS replacement and ALB cutover. Cloned pending events were quarantined before the worker started, so redelivery of events acknowledged by the source after the restore point was not exercised.

| Check | Observed result |
| --- | --- |
| Restored media and captions | Selected pre-existing synthetic MP4 decoded; its restored VTT contained cues |
| Password controls | Missing password 403, wrong password 403, correct password 200 |
| Fresh identity | Real PEM SRP and MFA sign-in in Chromium 151.0.7922.34; staff identity 200; missing-token denial 401 and authenticated restored profile 200 |
| Isolated S3 recovery | Recovered an earlier version of the copied synthetic media and decoded the exact original bytes |
| Retained event after restart | Two HMAC-verified attempts retained identical event bytes, produced one receiver effect and ended with an acknowledged outbox row |
| Cleanup | Removed the owned restore, isolated bucket and all eight object versions, temporary security group, local app, receiver, tunnel and test browser. Readback confirmed source database availability, original security group and storage settings |

The original fixture is legacy media version 0. The test set a new password only in the clone because the original plaintext was unavailable. The event was seeded after restoration, before application restart. This does not prove original-password acceptance or survival of an original pre-PITR pending event. Source staging storage is unversioned; the version test used a new isolated bucket. The 319-second point age is not a measurement of lost acknowledged writes. These staging timings do not establish production recovery guarantees or verification of the newer candidate.

## Browser evidence

All media and accounts in these checks were synthetic. Chromium and Firefox ran on Ubuntu 26.04. WebKit ran in an already-cached Playwright 1.62.1 container on Ubuntu 24.04.4. Linux WebKit is not Safari on macOS or iOS.

| Browser | Passed checks | Limits |
| --- | --- | --- |
| Chromium 151.0.7922.34 | Real staff/native sign-in and MFA; synthetic camera-only and screen-plus-camera recording, pause/resume and viewport change; browser-enforced camera/microphone denial with no recording; MP4/WebM upload after network interruption, two recordings without duplication and renewal of the pending upload URL; protected watch/embed, wrong-password rejection, actual playback, three caption cues and resume after a 310-second pause | Synthetic devices and viewport resizing do not establish real-device capture or a full-screen resolution change |
| Firefox 153.0 | Synthetic camera-only recording with pause/resume; interrupted MP4/WebM upload with pending-upload renewal and no duplicate recordings; protected watch/embed, wrong-password rejection, actual playback, three caption cues and resume after a 310-second pause | Screen capture and device-denial cases remain unverified in this engine |
| WebKit 26.5, build 2336 | Protected watch and embed, missing/wrong-code denial, actual playback and caption cues; watch renewed its media URL and resumed after a 310-second pause | No real Safari or device capture |

Initial harness attempts used the legacy password selector on the managed watch page, which uses a video-code field. Parallel negative checks also reached a rate limit. Those attempts do not establish product failures. Corrected, spaced checks retained the denial assertions. Firefox needed isolated codec libraries; the host WebKit bundle failed dependency preflight. The cached matching container passed without changing system libraries or bypassing that check.

The versioned real-device Chrome/Safari/Firefox matrix remains open. HQ's earlier report of Sami's successful real-camera/Safari pass remains operator-reported evidence without per-case browser/OS versions. Keep the missing cases in the rollout gate list.

## GitHub release gate

The `production` environment now requires `sami-abdul` or `Sunnndayyy` to approve. It has no wait timer, refuses self-review and permits the `main` branch only. These settings were read back from GitHub. Creating the environment did not approve or run a production deployment.

The repository and staging environment still have no release workflow variables or configured release-role references. The staging identity is denied listing or reading GitHub OIDC provider configuration. Staging used the documented manual CDK and promotion path. The GitHub role references and permissions still need verification and configuration before workflow dispatch. A named reviewer setting is not a working production release role.

## Remaining production gates

1. **Window A approval.** Obtain the decision to include SendRec, Josh's go and Sami's production maintenance grant. Read the current Window A commands and reviewed diff, with the final staging-verified digest.
2. **Window A empty stack.** In the grant window, configure and confirm production alert delivery, copy that digest unchanged, review the empty stack, storage versioning and indefinite retention, and verify the approved infrastructure and recovery settings. Keep application tasks at zero. This phase can proceed under its own approval while later acceptance conditions remain open.
3. **Worker gate, passed for the tested configuration.** The same candidate passed transcription reclaim, outbox delivery, the separate video stuck-processing reset and thumbnail recovery across two tasks. Eligible-object cleanup passed during the interruption drill; concurrent cleanup by two tasks was not established. Managed-retention refusal and media survival passed through task interruption with optional sending workers and legacy webhooks disabled. Preserve those settings. Enabling optional retention or sending requires separate recovery and deduplication evidence; this gate does not cover that mode.
4. **Two-task rollout.** Standard production promotion requires exactly two tasks. It waits for the explicit rollout approval for the exact digest and preservation of gate 3's tested settings. It also needs the final template-pair hash and matching staging receipt. The script does not enforce receipt expiry; use a fresh standard run and reject an older or mismatched receipt during operator review. Verify dependency readiness, task replacement, backups, and both fault and recovery email receipt. Integration stays disabled until patient activation is separately approved.
5. **Before patient activation.** Complete the missing versioned real-device browser cases and resolve the remaining production recovery and service-token rotation conditions. Empty-stack preparation or the two-task rollout does not establish those acceptance conditions.
6. **Patient activation.** Complete the production identity/client, secrets and mpd-api activation steps in their approved window, then run the synthetic end-to-end acceptance check. Preserve Vimeo and retained resources. Inventory, matching, import and link verification precede any separately approved Vimeo retirement.

No production command ran during this staging work. MPD-187 and MPD-213 remain In Progress.

## Final staging state and evidence

At 11:46:31 UTC, staging was back at one running application task, zero pending tasks and one healthy target on definition 18. The target drain remained 30 seconds. Health reported `5859b8dc`; database, storage and workers were ready. Both dedicated synthetic test accounts were disabled, including provider readback for the primary account. Their synthetic media remains under the managed retention policy. No shared membership or original fixture was removed.

The additional worker drill temporarily returned staging to two tasks. At 12:29:39 UTC it was again at one running definition-18 task, zero pending tasks and one healthy target, with the 30-second drain and all readiness dependencies passing. The seeded warning was restored to null after verification. The owned orphan is gone; the synthetic recordings remain retained. No shared account or policy was changed.

The private worker evidence directory retains promotion, restore and cleanup receipts, browser results, readiness probes, the abandoned-task record, final read-only generation/outbox snapshots and media/caption checks. It also contains private test sessions and signed links, so it must not be uploaded or committed as a directory. This dated record is the sanitized review artifact. The private Window A preparation copy names future commands and the actual template-pair/receipt prerequisites; it authorizes none of them.
