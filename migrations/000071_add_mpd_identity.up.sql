-- No existing workspace or media is adopted by this migration.
CREATE TABLE mpd_managed_workspaces (
 organization_id UUID PRIMARY KEY REFERENCES organizations(id) ON DELETE RESTRICT,
 tenant_id UUID NOT NULL UNIQUE,
 issuer TEXT NOT NULL,
 enabled BOOLEAN NOT NULL DEFAULT false,
 seat_limit INTEGER NOT NULL CHECK (seat_limit > 0),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE mpd_external_identities (
 issuer TEXT NOT NULL,
 subject TEXT NOT NULL,
 user_id UUID NOT NULL UNIQUE REFERENCES users(id) ON DELETE RESTRICT,
 organization_id UUID NOT NULL REFERENCES mpd_managed_workspaces(organization_id) ON DELETE RESTRICT,
 staff_id UUID NOT NULL,
 tenant_id UUID NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY (issuer,subject),
 UNIQUE (tenant_id,staff_id)
);
CREATE TABLE mpd_sessions (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 user_id UUID NOT NULL REFERENCES mpd_external_identities(user_id) ON DELETE RESTRICT,
 access_encrypted TEXT NOT NULL,
 refresh_encrypted TEXT NOT NULL DEFAULT '',
 refresh_hash TEXT UNIQUE,
 classification_hash TEXT NOT NULL UNIQUE,
 classification_expires_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 cognito_expires_at TIMESTAMPTZ NOT NULL,
 expires_at TIMESTAMPTZ NOT NULL,
 grant_json JSONB NOT NULL,
 grant_expires_at TIMESTAMPTZ NOT NULL,
 revoked BOOLEAN NOT NULL DEFAULT false,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX mpd_sessions_user ON mpd_sessions(user_id);
CREATE TABLE mpd_login_transactions (
 state_hash TEXT PRIMARY KEY,
 browser_hash TEXT NOT NULL,
 nonce TEXT NOT NULL,
 verifier_encrypted TEXT NOT NULL,
 callback TEXT NOT NULL,
 expires_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE mpd_login_handoffs (
 code_hash TEXT PRIMARY KEY,
 browser_hash TEXT NOT NULL,
 session_id UUID NOT NULL REFERENCES mpd_sessions(id) ON DELETE CASCADE,
 expires_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE mpd_identity_audit (
 id BIGSERIAL PRIMARY KEY,
 actor TEXT NOT NULL,
 action TEXT NOT NULL,
 user_id UUID,
 organization_id UUID,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Database guards cover legacy SSO/SCIM and refresh paths, including old tokens.
CREATE FUNCTION mpd_guard_local_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' AND EXISTS(SELECT 1 FROM mpd_external_identities WHERE user_id=OLD.user_id) THEN
  RAISE EXCEPTION 'managed identity requires MPD sign in';
 END IF;
 IF EXISTS(SELECT 1 FROM mpd_external_identities WHERE user_id=NEW.user_id) THEN
  RAISE EXCEPTION 'managed identity requires MPD sign in';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER mpd_no_local_refresh BEFORE INSERT ON refresh_tokens FOR EACH ROW EXECUTE FUNCTION mpd_guard_local_identity();
CREATE TRIGGER mpd_no_alternate_identity BEFORE INSERT OR UPDATE ON external_identities FOR EACH ROW EXECUTE FUNCTION mpd_guard_local_identity();
CREATE FUNCTION mpd_guard_membership() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE u UUID; o UUID;
BEGIN
 IF TG_OP='DELETE' THEN u=OLD.user_id;o=OLD.organization_id; ELSE u=NEW.user_id;o=NEW.organization_id; END IF;
 IF TG_OP='UPDATE' AND coalesce(current_setting('sendrec.mpd_provision',true),'') <> '1' AND
 (EXISTS(SELECT 1 FROM mpd_managed_workspaces WHERE organization_id=OLD.organization_id) OR EXISTS(SELECT 1 FROM mpd_external_identities WHERE user_id=OLD.user_id)) THEN
 RAISE EXCEPTION 'managed membership is deployment controlled';
 END IF;
 IF coalesce(current_setting('sendrec.mpd_provision',true),'') <> '1' AND
 (EXISTS(SELECT 1 FROM mpd_managed_workspaces WHERE organization_id=o) OR EXISTS(SELECT 1 FROM mpd_external_identities WHERE user_id=u)) THEN
  RAISE EXCEPTION 'managed membership is deployment controlled';
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;RETURN NEW;
END $$;
CREATE TRIGGER mpd_membership_boundary BEFORE INSERT OR UPDATE OR DELETE ON organization_members FOR EACH ROW EXECUTE FUNCTION mpd_guard_membership();
-- Existing personal media stays personal. This is not an adoption mechanism.
CREATE FUNCTION mpd_guard_video_transfer() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.organization_id IS DISTINCT FROM OLD.organization_id AND
 (EXISTS(SELECT 1 FROM mpd_managed_workspaces WHERE organization_id=OLD.organization_id) OR
 EXISTS(SELECT 1 FROM mpd_managed_workspaces WHERE organization_id=NEW.organization_id)) THEN
 RAISE EXCEPTION 'managed media transfer requires reviewed operator adoption';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER mpd_no_media_transfer BEFORE UPDATE OF organization_id ON videos FOR EACH ROW EXECUTE FUNCTION mpd_guard_video_transfer();
CREATE FUNCTION mpd_guard_user_lifecycle() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM mpd_external_identities WHERE user_id=OLD.id) THEN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'managed account retirement is deployment controlled'; END IF;
  IF NEW.retention_days IS DISTINCT FROM OLD.retention_days OR NEW.email IS DISTINCT FROM OLD.email OR NEW.password IS DISTINCT FROM OLD.password THEN
   RAISE EXCEPTION 'managed identity and retention are deployment controlled';
  END IF;
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF; RETURN NEW;
END $$;
CREATE TRIGGER mpd_user_lifecycle BEFORE UPDATE OR DELETE ON users FOR EACH ROW EXECUTE FUNCTION mpd_guard_user_lifecycle();
CREATE FUNCTION mpd_guard_workspace_lifecycle() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM mpd_managed_workspaces WHERE organization_id=OLD.id) THEN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'managed workspace retirement is deployment controlled'; END IF;
  IF NEW.retention_days IS DISTINCT FROM OLD.retention_days THEN RAISE EXCEPTION 'managed retention is deployment controlled'; END IF;
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF; RETURN NEW;
END $$;
CREATE TRIGGER mpd_workspace_lifecycle BEFORE UPDATE OR DELETE ON organizations FOR EACH ROW EXECUTE FUNCTION mpd_guard_workspace_lifecycle();
CREATE FUNCTION mpd_guard_workspace_identity() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE o UUID;
BEGIN
 IF TG_OP='DELETE' THEN o=OLD.organization_id; ELSE o=NEW.organization_id; END IF;
 IF EXISTS(SELECT 1 FROM mpd_managed_workspaces WHERE organization_id=o) THEN
  RAISE EXCEPTION 'managed SSO and SCIM are deployment controlled';
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;RETURN NEW;
END $$;
CREATE TRIGGER mpd_sso_boundary BEFORE INSERT OR UPDATE OR DELETE ON organization_sso_configs FOR EACH ROW EXECUTE FUNCTION mpd_guard_workspace_identity();
CREATE TRIGGER mpd_scim_boundary BEFORE INSERT OR UPDATE OR DELETE ON organization_scim_tokens FOR EACH ROW EXECUTE FUNCTION mpd_guard_workspace_identity();
CREATE FUNCTION mpd_guard_activation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  IF EXISTS(SELECT 1 FROM videos WHERE organization_id=NEW.organization_id) OR EXISTS(SELECT 1 FROM organization_members WHERE organization_id=NEW.organization_id) THEN
   RAISE EXCEPTION 'initial managed workspace must be empty';
  END IF;
 END IF;
 IF EXISTS(SELECT 1 FROM organizations WHERE id=NEW.organization_id AND retention_days<>0) THEN RAISE EXCEPTION 'managed workspace requires retention disabled'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER mpd_activation_boundary BEFORE INSERT OR UPDATE ON mpd_managed_workspaces FOR EACH ROW EXECUTE FUNCTION mpd_guard_activation();

CREATE FUNCTION mpd_guard_identity_binding() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM mpd_managed_workspaces WHERE organization_id=NEW.organization_id AND issuer=NEW.issuer AND tenant_id=NEW.tenant_id) THEN
 RAISE EXCEPTION 'identity issuer and tenant must match managed workspace';
 END IF;
 IF EXISTS(SELECT 1 FROM users WHERE id=NEW.user_id AND retention_days<>0) THEN
 RAISE EXCEPTION 'disable personal retention before managed identity linking';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER mpd_identity_binding BEFORE INSERT OR UPDATE ON mpd_external_identities FOR EACH ROW EXECUTE FUNCTION mpd_guard_identity_binding();
