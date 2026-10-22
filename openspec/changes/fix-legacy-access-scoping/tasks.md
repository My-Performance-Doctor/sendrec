## 1. Workspace identity

- [x] 1.1 Refuse the workspace SSO email link unless the existing account already belongs to the workspace; test the refusal and the member case.
- [x] 1.2 Return 404 from SCIM PATCH and PUT on users who are neither linked to nor members of the token's workspace, before any write; test both.

## 2. Video scope

- [x] 2.1 Make batch tags return 404 when the scoped count differs from the deduplicated ID count, and scope its DELETE and INSERT; test another user's video.
- [x] 2.2 Add `organization_id IS NULL` to the personal-scope batch, folder, tag, notification and analytics paths; test each.
