## 1. Event contract and acceptance

- [x] 1.1 Publish schemaVersion 1 fixtures for ready, transcript-ready, playback-start and deletion events; verify the mpd-api consumer agrees on fields, version and viewer classes. Test that unlinked evaluation media emits no managed event and admission fails before any managed recording without trusted owner/tenant bindings.
- [x] 1.2 Add server-bound playback sessions and reuse player progress to record first actual play; test watch/embed success and page-open, preload, seek-only, stalled and password-denied negatives.
- [ ] 1.3 Derive staff_preview/recipient/anonymous from verified context; implement the separate native classification cookie and origin-bound embed handoff. Test forged classes, replayed sessions, direct native watch after session renewal, future-client top-level preview and staff embeds with third-party cookies disabled; signed-out visits remain anonymous.

## 2. Durable producer and transport

- [x] 2.1 Add outbox, leases, attempt state and unique playback fact storage; test transaction rollback, concurrent workers and restart after commit before send.
- [x] 2.2 Inventory replacement, trim, remove-segments and silence-removal paths and bump media versions at mutation acceptance. Allocate transcript generations for jobs and direct upload/edit at acceptance and reject stale completions; test concurrent retranscriptions finishing in reverse order. Assign mediaVersion before upload and include delete-before-ready fixtures. Publish readiness, caption version, playback and deletion events atomically with their state changes; inventory single/batch delete, account/workspace removal, retention and cleanup. Verify blocked managed lifecycle paths, durable tombstone/outbox before purge, event survival after owner removal and refusal of stale media versions.
- [x] 2.3 Configure a deployment-controlled MPD receiver separately from user notifications; verify disabled personal notifications do not stop managed events and arbitrary user URLs retain SSRF protection.
- [x] 2.4 Add timestamp/key-id HMAC signing, rotation overlap and immutable event replay; test tampered bodies, expired attempt timestamps and receiver commit with a lost response.
- [x] 2.5 Add durable backoff, 24-hour exhaustion, dead-letter visibility and replay; prove pending work survives task replacement and acknowledged evidence retains for 30 days.

## 3. Verification and delivery

- [ ] 3.1 Run a synthetic receiver outage/restart/replay drill with duplicate and reordered events; verify consumer effects occur once and backlog alarms fire.
- [x] 3.2 Run the existing webhook/player tests and new failure-case tests; verify legacy video.viewed remains page access and no sensitive payload enters logs.
- [ ] 3.3 Run strict OpenSpec validation and the required PR review/CI workflow; publish the event contract and safe activation/rollback procedure with measured results.

## Evidence on 2026-10-08

- Contract, atomic lifecycle, admission, playback negatives, ownership snapshots, outbox leases, rotation, replay and recovery have real PostgreSQL coverage in `internal/mpdevents/events_test.go` and `internal/video/mpd_lifecycle_db_test.go`. The actual producer bodies also passed the mpd-api consumer in the local cross-service drill.
- 1.3: Verified-context classification, native cookie renewal and one-use preview handoffs are implemented. Browser checks with third-party cookies disabled and future-client top-level playback remain open. No recipient grant is implemented or accepted.
- 3.1: The synthetic consumer drill passed lost-acknowledgement retry, duplicate effects, transcript replacement and deletion. Actual backlog alarm receipt remains open.
- 3.2: Final full Go and frontend check results are recorded in [verification](../../../docs/mpd/verification.md). Existing `video.viewed` keeps its page-access meaning. New event delivery logs contain IDs/status only.
- 3.3: Strict validation passed. GitHub access is restored and [implementation PR 4](https://github.com/My-Performance-Doctor/sendrec/pull/4) is open. Application, Docker build and infrastructure CI passed; formal review remains pending. Browser E2E and preview deployment skipped under the PR workflow. Local application checks and lint passed. Event delivery is disabled until human merge and staged activation. See the [runbook](../../../docs/production/README.md).
