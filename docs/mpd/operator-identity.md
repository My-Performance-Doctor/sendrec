# Operator identity linking

Normal sign-in provisions a new local account only after Cognito verification and a current MPD grant. An email collision returns HTTP 409. Matching email addresses never authorize linking an existing account.

This procedure is for a reviewed evaluation-owner link. It does not adopt media or activate production. Use a new, empty managed workspace. Leave evaluation recordings in their existing personal or workspace scope.

## Evidence before a link

Record the operator and approval reference in the access-controlled change record. Verify all of these facts against the target environment:

- The issuer and approved client belong to the staff Cognito pool.
- The subject comes from a cryptographically verified access token, including issuer, expiry, `token_use=access` and approved `client_id`. An email, username or unverified JWT decode is insufficient.
- A fresh call to `/api/v1/staff/video-access` with that same token grants access. Record its staff and tenant binding in the restricted evidence, without retaining the token.
- The local user ID is the approved evaluation account. Confirm ownership with the account holder. Email equality alone is insufficient.
- The target workspace has no media and no unlinked members. Its tenant and issuer match the evidence. The local account and workspace have `retention_days=0`. Review any retention change separately before linking.

For an existing Staff OS Cognito token, SendRec first tries OIDC userinfo. If the token has `aws.cognito.signin.user.admin` without `openid`, SendRec can use Cognito GetUser at the configured pool's regional endpoint. It requires a matching subject and verified email. This does not replace signature verification or the MPD grant. AWS documents the token scope and response attributes in [GetUser](https://docs.aws.amazon.com/cognito-user-identity-pools/latest/APIReference/API_GetUser.html).

## Prepare an empty workspace

Connect through the approved private operator database path. Load database credentials through the existing secret-injection mechanism and a protected PostgreSQL service/password file. Do not put a database URL, password or staff bearer token in command arguments, shell history or committed files. The service name below is a local connection profile, not a credential:

```sh
PGSERVICE=sendrec-operator psql -X -v ON_ERROR_STOP=1
```

Use interactive `\prompt` values. They never enter the shell command line. Choose a new workspace slug; a conflict must fail rather than reuse a workspace that might contain media.

```sql
\prompt 'Approved tenant UUID: ' tenant
\prompt 'Matching Cognito issuer: ' issuer
\prompt 'New workspace name: ' workspace_name
\prompt 'New workspace slug: ' workspace_slug
\prompt 'Reviewed seat limit: ' seat_limit
\prompt 'Operator and approval reference: ' operator_ref
BEGIN;
INSERT INTO organizations(name,slug,retention_days)
VALUES (:'workspace_name',:'workspace_slug',0)
RETURNING id AS workspace_id \gset
INSERT INTO mpd_managed_workspaces
  (organization_id,tenant_id,issuer,enabled,seat_limit)
VALUES (:'workspace_id',:'tenant'::uuid,:'issuer',false,:'seat_limit'::integer);
INSERT INTO mpd_identity_audit(actor,action,organization_id)
VALUES (:'operator_ref','operator_created_empty_workspace',:'workspace_id');
COMMIT;
```

The workspace stays disabled. Retain its identifier in the restricted deployment record. Confirm that the media count and member count are zero before continuing. The activation trigger also refuses a workspace containing either.

## Link the verified account

Recheck the evidence immediately before this transaction. The target workspace must still be disabled. This transaction links one existing account, invalidates its legacy refresh credentials and records an audit entry. It leaves role projection to the first authorized MPD login.

```sql
\prompt 'Target managed workspace UUID: ' workspace_id
\prompt 'Verified local user UUID: ' local_user_id
\prompt 'Verified Cognito subject: ' cognito_subject
\prompt 'Verified MPD staff UUID: ' staff_id
\prompt 'Verified MPD tenant UUID: ' tenant
\prompt 'Verified matching Cognito issuer: ' issuer
\prompt 'Operator and approval reference: ' operator_ref
BEGIN;
CREATE TEMP TABLE operator_binding ON COMMIT DROP AS
SELECT :'workspace_id'::uuid AS workspace_id,
       :'local_user_id'::uuid AS user_id,
       :'cognito_subject'::text AS subject,
       :'staff_id'::uuid AS staff_id,
       :'tenant'::uuid AS tenant_id,
       :'issuer'::text AS issuer,
       :'operator_ref'::text AS actor;
DO $$
DECLARE b operator_binding%ROWTYPE; n integer;
BEGIN
 SELECT * INTO STRICT b FROM operator_binding;
 IF b.subject='' OR b.actor='' THEN
  RAISE EXCEPTION 'verified subject and approval reference required';
 END IF;
 PERFORM 1 FROM mpd_managed_workspaces
 WHERE organization_id=b.workspace_id AND tenant_id=b.tenant_id
   AND issuer=b.issuer AND NOT enabled FOR UPDATE;
 IF NOT FOUND THEN RAISE EXCEPTION 'disabled workspace binding mismatch'; END IF;
 PERFORM 1 FROM users WHERE id=b.user_id AND retention_days=0 FOR UPDATE;
 IF NOT FOUND THEN RAISE EXCEPTION 'approved account missing or retention enabled'; END IF;
 IF EXISTS(SELECT 1 FROM videos WHERE organization_id=b.workspace_id)
 OR EXISTS(SELECT 1 FROM organization_members WHERE organization_id=b.workspace_id)
 OR EXISTS(SELECT 1 FROM mpd_external_identities WHERE user_id=b.user_id)
 THEN RAISE EXCEPTION 'workspace or account already linked; stop for review'; END IF;
 SELECT count(*) INTO n FROM mpd_external_identities
 WHERE organization_id=b.workspace_id;
 IF n >= (SELECT seat_limit FROM mpd_managed_workspaces
          WHERE organization_id=b.workspace_id)
 THEN RAISE EXCEPTION 'managed workspace seat capacity reached'; END IF;
END $$;
CREATE TEMP TABLE prior_media ON COMMIT DROP AS
SELECT id,organization_id FROM videos
WHERE user_id=(SELECT user_id FROM operator_binding);
INSERT INTO mpd_external_identities
  (issuer,subject,user_id,organization_id,staff_id,tenant_id)
SELECT issuer,subject,user_id,workspace_id,staff_id,tenant_id FROM operator_binding;
UPDATE refresh_tokens SET revoked=true
WHERE user_id=(SELECT user_id FROM operator_binding);
INSERT INTO mpd_identity_audit(actor,action,user_id,organization_id)
SELECT actor,'operator_linked_verified_identity',user_id,workspace_id FROM operator_binding;
DO $$
BEGIN
 IF EXISTS(
  (SELECT id,organization_id FROM videos
   WHERE user_id=(SELECT user_id FROM operator_binding)
   EXCEPT SELECT id,organization_id FROM prior_media)
  UNION ALL
  (SELECT id,organization_id FROM prior_media
   EXCEPT SELECT id,organization_id FROM videos
   WHERE user_id=(SELECT user_id FROM operator_binding))
 ) THEN RAISE EXCEPTION 'media scope changed during linking'; END IF;
END $$;
COMMIT;
```

Read back the identity binding and audit row using identifiers from the restricted record. Verify existing media retains its prior `organization_id`. Do not copy names, media content, tokens or signed URLs into the PR. Existing API keys and alternate logins must now fail managed authorization, including requests with no workspace header.

The application exposes no operator bypass session. The operator cannot use this procedure to issue staff tokens or skip the current MPD grant. Recovery consists of fixing reviewed configuration or binding records, then repeating normal MPD sign-in.

## Activate or roll back

Keep `MPD_ENABLED=false` and the workspace disabled until the producer deployment, matching environment/client configuration and synthetic access checks have passed. Activation requires the separate reviewed deployment step. Enable the workspace and MPD entry points only in that step; the first successful staff login creates membership using its current grant. Verify an authorized account succeeds and a revoked account fails within the bounded grant deadline.

For rollback, disable MPD entry points and the managed workspace, then revoke that workspace's `mpd_sessions`. Preserve external identity bindings, audit rows, media and publication state. Keep registration disabled. The persisted identity guards must continue rejecting legacy JWTs, API keys and local refresh attempts. Do not unlink identities to restore local access or move media through the ordinary transfer endpoint. Account retirement or later media adoption needs its own reviewed procedure.

This file is an operator procedure. It is not evidence that linking or activation ran in an environment.
