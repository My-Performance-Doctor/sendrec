## 1. Dependencies and identity

- [ ] 1.1 Confirm the reviewed mpd-api access contract and matching environment Cognito client configuration; verify synthetic contract fixtures match before activation.
- [ ] 1.2 Add unique external-identity and managed-session storage with reversible rollout steps; test concurrent first login, issuer isolation and existing-email collision without takeover.
- [ ] 1.3 Adapt the native OIDC entry and callback with state, nonce, PKCE and a one-use handoff code; test replay, wrong callback, failed membership and absence of bearer tokens in redirects/logs.
- [ ] 1.4 Add the future-client token exchange using the same authorization/provisioning path; verify a synthetic client resolves to the same account and rejects unapproved origins and audiences.

## 2. Enforcement and media integration

- [ ] 2.1 Project MPD capabilities into workspace roles and enforce them across management routes; test personal context, header tampering, API keys, local passwords and alternate SSO bypass attempts. Refuse managed account/workspace deletion and retention changes; verify offboarding retains media.
- [ ] 2.2 Implement bounded access checks and native/future-client renewal rules; verify expired grants fail closed and offboarding blocks existing sessions within 60 seconds.
- [ ] 2.3 Add hashed scoped MPD service credentials and metadata/transcript/password/publication endpoints; test allowed actions, token rotation, wrong-workspace denial and forbidden deletion/impersonation.
- [ ] 2.4 Add managed unpublished state and enforce it on watch/embed, captions, thumbnails and downloads; test that a guessed share token cannot bypass it and publication preserves password controls.
- [ ] 2.5 Cap newly issued signed media URLs at five minutes and protect credential storage; implement authorized URL renewal that preserves playback position and paused state. Verify long playback, pause/seek across expiry, denied renewal and redacted errors with synthetic fixtures.
- [ ] 2.6 Add the standalone MPD sign-in entry and explicit operator identity-linking procedure; verify the recorder works and a matching email alone cannot transfer evaluation-owner media.

## 3. Compatibility and handoff

- [ ] 3.1 Run native recording, MP4/WebM upload, protected watch/embed, captions and retranscription under MPD sessions; retain actual results and capacity-limit behavior.
- [ ] 3.2 Document the recording API and service adapter with synthetic requests and failure responses; verify a standalone API client can exercise it without Staff OS screens.
- [ ] 3.3 Run relevant Go, frontend and infrastructure checks and strict OpenSpec validation; verify the feature is disabled until its producer dependency is deployed.
- [ ] 3.4 Open the implementation PR with login UI evidence, complete review and CI, and document staged activation/rollback; keep production activation pending its separate approval.
