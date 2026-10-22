## ADDED Requirements

### Requirement: Workspace SSO links only workspace members by email
Workspace SSO SHALL link an external identity to an existing account found by email only when that account is already a member of the workspace.

#### Scenario: Non-member account with the asserted email
- **WHEN** a workspace's OIDC or SAML provider asserts the email of an existing verified account that is not a member of that workspace and has no link to it
- **THEN** the sign-in fails, no identity link is stored and no tokens are issued

#### Scenario: Member account with the asserted email
- **WHEN** the asserted email belongs to an existing verified member of the workspace
- **THEN** the identity is linked and the member signs in

### Requirement: SCIM user updates stay in the token's workspace
SCIM PATCH and PUT on a user SHALL return 404 without writing unless the user is linked to, or a member of, the token's workspace.

#### Scenario: User from another workspace
- **WHEN** a SCIM client for workspace A updates a user who has no link to and no membership in workspace A
- **THEN** the response is 404 and the user is unchanged

### Requirement: Batch tag updates stay in the caller's scope
`POST /api/videos/batch/tags` SHALL change tags only when every requested video is in the caller's scope.

#### Scenario: Another user's video in the batch
- **WHEN** the request includes a video the caller may not change
- **THEN** the response is 404 and no tags change on any video

### Requirement: Personal scope excludes workspace videos
Personal-scope video writes and reads SHALL apply only to videos with no workspace, as `orgRowFilter` does.

#### Scenario: Removed or demoted member without a workspace header
- **WHEN** a user calls batch delete, batch folder, batch tags, folder, tag, notification or analytics paths without `X-Organization-Id` on a video they recorded in a workspace
- **THEN** the video is treated as not found and is unchanged
