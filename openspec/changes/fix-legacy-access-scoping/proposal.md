## Why

The 22 October 2026 security audit confirmed four ways a legacy (non-managed) SendRec account can reach accounts or videos it should not: workspace SSO links any verified account by email, a workspace's SCIM token can update any non-managed user, batch tag updates touch videos the caller does not own, and several personal-scope video paths ignore `organization_id IS NULL`, so removed or demoted workspace members can still act on workspace videos. Managed MPD staff are protected by database triggers, but legacy operator accounts are not.

## What Changes

- Workspace SSO (OIDC and SAML) links an existing account by email only when that account is already a member of the workspace. Social sign-in keeps its current behaviour.
- SCIM PATCH and PUT on `/Users/{id}` return 404 unless the user is linked to, or a member of, the token's workspace, before any write.
- `POST /api/videos/batch/tags` returns 404 unless every requested video is in the caller's scope, and its DELETE and INSERT carry the same scope predicate.
- Personal-scope batch delete, batch folder, batch tags, folder, tag, notification and analytics paths require `organization_id IS NULL`, matching `orgRowFilter`.
- A regression test for each.

## Capabilities

### New Capabilities

- `legacy-access-scoping`: workspace SSO, SCIM and personal-scope video writes stay inside the caller's workspace or personal scope.

### Modified Capabilities

None.

## Impact

`internal/sso`, `internal/scim` and `internal/video` (batch, folder, tag, notification, analytics) with their tests. No schema change and no API shape change; out-of-scope requests now get 404 or an SSO error instead of succeeding.

## Approval and delivery

Sami approved these fixes from the audit report on 2026-10-22 and waived PR review for this change.
