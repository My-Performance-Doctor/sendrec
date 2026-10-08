## 1. Event contract and acceptance

- [ ] 1.1 Publish schemaVersion 1 fixtures for ready, transcript-ready, playback-start and deletion events; verify the mpd-api consumer agrees on fields, version and viewer classes. Test that unlinked evaluation media emits no managed event and admission fails before any managed recording without trusted owner/tenant bindings.
- [ ] 1.2 Add server-bound playback sessions and reuse player progress to record first actual play; test watch/embed success and page-open, preload, seek-only, stalled and password-denied negatives.
- [ ] 1.3 Derive staff_preview/recipient/anonymous from verified context; implement the separate native classification cookie and origin-bound embed handoff. Test forged classes, replayed sessions, direct native watch after session renewal, future-client top-level preview and staff embeds with third-party cookies disabled; signed-out visits remain anonymous.

## 2. Durable producer and transport

- [ ] 2.1 Add outbox, leases, attempt state and unique playback fact storage; test transaction rollback, concurrent workers and restart after commit before send.
- [ ] 2.2 Allocate transcript generations at request time and reject stale completions; test concurrent retranscriptions finishing in reverse order. Assign mediaVersion before upload and include delete-before-ready fixtures. Publish readiness, caption version, playback and deletion events atomically with their state changes; inventory single/batch delete, account/workspace removal, retention and cleanup. Verify blocked managed lifecycle paths, durable tombstone/outbox before purge, event survival after owner removal and refusal of stale media versions.
- [ ] 2.3 Configure a deployment-controlled MPD receiver separately from user notifications; verify disabled personal notifications do not stop managed events and arbitrary user URLs retain SSRF protection.
- [ ] 2.4 Add timestamp/key-id HMAC signing, rotation overlap and immutable event replay; test tampered bodies, expired attempt timestamps and receiver commit with a lost response.
- [ ] 2.5 Add durable backoff, 24-hour exhaustion, dead-letter visibility and replay; prove pending work survives task replacement and acknowledged evidence retains for 30 days.

## 3. Verification and delivery

- [ ] 3.1 Run a synthetic receiver outage/restart/replay drill with duplicate and reordered events; verify consumer effects occur once and backlog alarms fire.
- [ ] 3.2 Run the existing webhook/player tests and new failure-case tests; verify legacy video.viewed remains page access and no sensitive payload enters logs.
- [ ] 3.3 Run strict OpenSpec validation and the required PR review/CI workflow; publish the event contract and safe activation/rollback procedure with measured results.
