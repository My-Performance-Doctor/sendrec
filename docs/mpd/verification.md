# Verification and remaining activation checks

This implementation uses synthetic identities and media. It has not enabled staging identity integration or deployed production.

The local cross-service drill runs the actual Go producer/outbox/service adapter and the actual mpd-api PR 211 webhook, inbox, claims and report processing on disposable PostgreSQL databases. It stubs media/LLM enrichment and external task delivery. It passed these checks on 2026-10-08:

- The consumer durably accepted an event, the response was lost, and retry created one receipt/effect.
- Claiming created one report and timeline entry.
- Staff preview left watched false; duplicate anonymous playback set it true once.
- A second transcript updated the existing report.
- Deletion removed playback URLs while retaining the clinical timeline.
- Five actual SQL-produced event bodies validated unchanged through the consumer model.

Reproduce with `scripts/tests/test_mpd_cross_service.py` and the synthetic producer harness in `internal/video/mpd_cross_service_test.go`. The test source documents its required sibling checkout and environment. Do not point either database setting or receiver at a live service.

Local identity tests cover verified issuer/client/signature, concurrent first login, email collision, PKCE and callback replay, expired/outage grants, offboarding, capability/ownership checks and alternate-login/provisioning guards. Media tests cover version races, transcript generations, durable deletion, service work fences and retained effect receipts. JavaScript tests exercise first actual play, stalled/seek negatives, paused/playing URL renewal, denied renewal and playlist item changes. These are automated simulations, not a browser matrix.

The independent implementation review found three issues: rollback could disable persisted public policy; another-tab reauthentication could leave retained uploads unable to refresh; playlist tracking stopped after its first item. All three have code fixes and focused regressions. The standard PR review loop runs after delivery.

The local PostgreSQL fixture eventually used asynchronous commits and disabled fsync because repeated per-test database creation/deletion stalled on host checkpoints and timed out one broader run. The tests prove application transaction, lease and process-recovery behavior. They do not prove database/host crash durability. The separate local restore drill ran before that fixture adjustment and its limits are recorded in the production runbook.

T3 browser initialization failed because the host's AppArmor configuration blocks its browser sandbox. No restraint was changed. Before/after screenshots, real Chrome/Firefox media capture and Safari checks remain unverified. Complete them on a supported host; a passing unit test or build does not replace those checks.

Activation still needs the human-merged mpd-api and SendRec revisions, matching Cognito client/issuer and secret references, an empty managed workspace, actual staged recording/offboarding checks, tested alert receipt and an isolated AWS RDS/S3 restore. Production configuration/cost and recovery objectives require review, followed by separate rollout approval. The existing staging change stays open until its missing evidence is supplied honestly. Vimeo and evaluation media remain in place.

## Automated check results

- `go test ./... -count=1 -timeout=20m`, with PostgreSQL 18 and ffmpeg: 1,787 checks passed. The optional cross-service harness skipped in that command; its dedicated consumer run passed separately.
- Frontend suite: 887 tests passed across 47 files. The later focused player/client run passed 21 tests, including four renewals over a simulated sixteen-minute pause. Frozen dependency installation and TypeScript/Vite build passed. Vite reports the existing main bundle above its 500 kB warning threshold.
- Infrastructure: 15 Python tests, six Java tests and bootstrap access checks passed.
- Strict validation passed for all three approved changes.

The initial broad Go run failed four lifecycle fixtures because they enabled personal retention before linking a managed identity. The corrected fixture uses valid admission. Its regression also rejects both managed retention changes and proves that the worker still deletes eligible unmanaged media while retaining managed media with stale retention data.

Supplemental tests verify native/future same-account login, actual-route rejection of legacy credentials, service-token rotation, and standalone MP4/WebM HTTP recording with protected preview and captions. The last flow uses synthetic grants and memory storage. It does not establish real S3 or browser compatibility.

Managed upload links now sign `If-None-Match: *`. Actual AWS SDK tests verify the signed condition; the synthetic object service rejects overwrite and missing headers. Upload recovery checks size/type and never treats storage outages as missing objects. The client can recover a lost PUT or finalization acknowledgement without repeating the object write, ready event or queued jobs, including at the monthly creation limit. Go lint reports zero issues.

The final expanded Go run passed 1,806 checks and hit one stale test fixture compiled before its last correction. The failure was `ordinary storage changed: "" map[] <nil>` because the ordinary-storage positive control had no URL configured. The corrected control passed its focused rerun. The complete video-package rerun then passed 1,060 checks, with only the separately exercised cross-service harness skipped. All other packages passed the expanded full run.

## Delivery

The operator refreshed GitHub access on 2026-10-08. The verified `workflow` scope allowed the full branch, including release workflows, to push. [Implementation PR 4](https://github.com/My-Performance-Doctor/sendrec/pull/4) is open. At implementation revision `58504bc8116d259b49672e0e69597f69d34e2ff5`, Application, Docker build and AWS infrastructure CI passed. The application job included Go race/coverage tests and vulnerability scanning. Browser E2E and preview deployment skipped under the PR workflow. The review helper failed all three attempts; recovered findings have fixes, but no clean formal round is recorded. Final-revision checks are tracked in the PR. No deployment occurred.


## PR review corrections

The first review invocation inherited the upstream repository selection and was stopped. The next invocation reviewed the MPD fork, but the helper rejected its fenced JSON array and saved only a truncated excerpt. The complete ten-finding report was recovered from that exact reviewer session. Neither failed invocation counts as a completed review round.

Corrections address backlog-driven readiness failure, managed SCIM target checks, Cognito verified-email decoding, retryable native refresh, stale cookies after changing login type, legacy upload cleanup, managed-only URL expiry and player renewal, expired temporary access records, and per-recording library queries. Persistent authorization checks remain active during rollback. The suggestion to bypass them based on disabled configuration was rejected because retained managed identities and media still require protection.

The final allowed attempt used the same Claude review at high effort through a temporary JSON-format adapter. The reviewer added prose before its JSON array, so the strict adapter preserved the output and the helper again failed. The full report was recovered. All three actual invocations are exhausted, with zero formal rounds recorded and no clean-review claim. Global review tooling was not changed.

The final report prompted fixes for database-only migration startup, failed-edit publication and caption recovery, publish-then-copy UI state, recent failure metrics, dormant-service alarm handling, public-route limits, tenant-scoped staff classification and redundant delivery/renewal queries. Failed edits retain their reserved media version to fence stale jobs. Migration 74 restores unchanged media availability atomically, while explicit password or publication changes still win. Persisted authorization checks remain active. Final correction checks are recorded in the PR.

Correction verification: the full Go suite passed, with the optional cross-service harness skipped. Identity/auth packages passed again after the last refresh-savepoint edit. All 892 frontend tests, TypeScript/Vite build, Go lint, staged secret scan and scope checks passed. The existing Vite bundle-size warning remains.

Final recovered-finding checks: 1,857 Go tests passed, one optional cross-service harness skipped. The migration entrypoint test passed again after its cleanup-error checks. Go lint reports zero issues. The affected detail page passed 115 tests and TypeScript/Vite build passed. Infrastructure passed 16 Python and six Java tests with synthesized alarm readback. Staged secret and scope checks passed.
