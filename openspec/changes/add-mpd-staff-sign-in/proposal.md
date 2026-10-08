## Why

Staff need individual MPD identities in the working standalone recorder. SendRec already supplies recording, storage and OIDC, but its account and session flows do not enforce MPD membership or support the agreed future Staff OS entry point.

## What Changes

- Reuse SendRec OIDC with the matching environment's MPD staff Cognito pool and a dedicated app client.
- Provision a linked local recording account only after the MPD access contract authorizes the caller; support an approved Staff OS token exchange through the same checks.
- Enforce MPD permissions and bounded offboarding on every managed recording route, including refresh and existing alternate authentication paths.
- Preserve native recording, upload, watch, embed and caption functionality, with a documented API contract and synthetic client tests.
- Add a narrowly scoped backend credential for mpd-api to retrieve media metadata and captions and apply existing password controls and publish a managed recording.

## Capabilities

### New Capabilities

- `mpd-staff-sign-in`: Reuse SendRec OIDC with the matching environment's MPD staff Cognito pool and a dedicated app client.

### Modified Capabilities

None in the current main spec store. Integration behavior is defined by the new capability; existing provider-specific requirements remain in force.

## Impact

Go auth, SSO and video middleware, a small standalone login change, identity/session migrations, and Java CDK settings. Depends on mpd-api `add-sendrec-staff-access`. Clinical mapping stays in mpd-api. Josh's Staff OS frontend, a new media backend and production activation are excluded.

## Approval and delivery

This proposal is one of five changes requested on 2026-10-08. Proposal review and explicit apply authorization precede implementation. Each implementation change lands through a human-merged PR. All examples and tests use synthetic identities and media.
