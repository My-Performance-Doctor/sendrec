-- Retained integration evidence deliberately has no cascading foreign keys.
CREATE TABLE mpd_video_state (
 video_id UUID PRIMARY KEY,
 owner_staff_id UUID NOT NULL,
 tenant_id UUID NOT NULL,
 media_version INTEGER NOT NULL CHECK(media_version > 0),
 transcript_version INTEGER NOT NULL DEFAULT 0,
 published BOOLEAN NOT NULL DEFAULT false,
 published_at TIMESTAMPTZ,
 work_version BIGINT NOT NULL DEFAULT 0,
 deleted_at TIMESTAMPTZ
);
ALTER TABLE videos ADD COLUMN transcript_generation INTEGER NOT NULL DEFAULT 0;
ALTER TABLE videos ADD COLUMN transcript_published_generation INTEGER NOT NULL DEFAULT 0;
CREATE TABLE mpd_event_outbox (
 event_id UUID PRIMARY KEY,
 video_id UUID NOT NULL,
 event_type TEXT NOT NULL,
 media_version INTEGER NOT NULL,
 dedup_key TEXT NOT NULL UNIQUE,
 body BYTEA NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','leased','acknowledged','blocked','dead')),
 attempts INTEGER NOT NULL DEFAULT 0,
 available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 lease_token UUID,
 lease_until TIMESTAMPTZ,
 acknowledged_at TIMESTAMPTZ,
 last_status INTEGER,
 replayed_at TIMESTAMPTZ
);
CREATE INDEX mpd_outbox_due ON mpd_event_outbox(available_at) WHERE state IN ('pending','leased');
CREATE TABLE mpd_playback_sessions (
 session_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 video_id UUID NOT NULL,
 media_version INTEGER NOT NULL,
 viewer_class TEXT NOT NULL CHECK(viewer_class IN ('anonymous','staff_preview','recipient')),
 expires_at TIMESTAMPTZ NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 started_at TIMESTAMPTZ,
 UNIQUE(session_id,video_id,media_version)
);
CREATE TABLE mpd_playback_facts (
 session_id UUID PRIMARY KEY,
 video_id UUID NOT NULL,
 media_version INTEGER NOT NULL,
 occurred_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE FUNCTION mpd_enqueue_event(p_video UUID,p_type TEXT,p_version INTEGER,p_data JSONB,p_dedup TEXT) RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE s mpd_video_state; eid UUID := gen_random_uuid(); stamp TIMESTAMPTZ := clock_timestamp();
BEGIN
 SELECT * INTO STRICT s FROM mpd_video_state WHERE video_id=p_video;
 INSERT INTO mpd_event_outbox(event_id,video_id,event_type,media_version,dedup_key,body)
 VALUES(eid,p_video,p_type,p_version,p_dedup,convert_to(jsonb_build_object(
 'schemaVersion',1,'eventId',eid,'eventType',p_type,'occurredAt',to_char(stamp AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
 'videoId',p_video,'mediaVersion',p_version,'ownerStaffId',s.owner_staff_id,'tenantId',s.tenant_id,'data',p_data)::text,'UTF8'))
 ON CONFLICT(dedup_key) DO NOTHING;
END $$;
CREATE FUNCTION mpd_video_before() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE ident mpd_external_identities; managed BOOLEAN;
BEGIN
 IF TG_OP='INSERT' THEN
  SELECT EXISTS(SELECT 1 FROM mpd_managed_workspaces WHERE organization_id=NEW.organization_id)
      OR EXISTS(SELECT 1 FROM mpd_external_identities WHERE user_id=NEW.user_id) INTO managed;
  IF managed THEN
   SELECT i.* INTO ident FROM mpd_external_identities i JOIN mpd_managed_workspaces w ON w.organization_id=i.organization_id
    WHERE i.user_id=NEW.user_id AND i.organization_id=NEW.organization_id AND i.tenant_id=w.tenant_id AND i.issuer=w.issuer AND w.enabled;
   IF NOT FOUND THEN RAISE EXCEPTION 'managed video requires verified staff and tenant binding'; END IF;
   NEW.media_version := 1;
   INSERT INTO mpd_video_state(video_id,owner_staff_id,tenant_id,media_version) VALUES(NEW.id,ident.staff_id,ident.tenant_id,1);
  END IF;
  RETURN NEW;
 END IF;
 SELECT EXISTS(SELECT 1 FROM mpd_video_state WHERE video_id=OLD.id) INTO managed;
 IF NOT managed THEN RETURN NEW; END IF;
 IF NEW.user_id IS DISTINCT FROM OLD.user_id OR NEW.organization_id IS DISTINCT FROM OLD.organization_id THEN
  RAISE EXCEPTION 'managed media ownership is immutable';
 END IF;
 IF OLD.status='deleted' AND NEW.status<>'deleted' THEN RAISE EXCEPTION 'managed video tombstone is permanent'; END IF;
 IF OLD.share_password IS NOT NULL AND NEW.share_password IS NULL THEN
  UPDATE mpd_video_state SET published=false,published_at=NULL WHERE video_id=NEW.id;
 END IF;
 -- In-place edits claim their new version before processing starts. Conversion
 -- workers do not enter processing from ready and keep the content version.
 IF OLD.status='ready' AND NEW.status='processing' THEN
  NEW.media_version := OLD.media_version + 1;
  NEW.transcript_generation := 0;
  NEW.transcript_published_generation := 0;
  NEW.transcript_status := 'none';
 ELSIF OLD.status='processing' AND NEW.status='ready' AND NEW.media_version=OLD.media_version+1 THEN
  -- Upstream's edit completion also increments. The version is already reserved.
  NEW.media_version := OLD.media_version;
 END IF;
 IF NEW.media_version < OLD.media_version THEN RAISE EXCEPTION 'managed media version cannot decrease'; END IF;
 IF NEW.media_version <> OLD.media_version THEN
  UPDATE mpd_video_state SET media_version=NEW.media_version,transcript_version=0,published=false,published_at=NULL WHERE video_id=NEW.id;
 END IF;
 IF NEW.transcript_status='pending' AND OLD.transcript_status IS DISTINCT FROM 'pending'
    AND NEW.transcript_generation=OLD.transcript_generation THEN
  NEW.transcript_generation := OLD.transcript_generation+1;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER mpd_video_before_trigger BEFORE INSERT OR UPDATE ON videos FOR EACH ROW EXECUTE FUNCTION mpd_video_before();
CREATE FUNCTION mpd_video_after() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE vid UUID; ver INTEGER;
BEGIN
 IF TG_OP='DELETE' THEN vid:=OLD.id; ELSE vid:=NEW.id; END IF;
 SELECT media_version INTO ver FROM mpd_video_state WHERE video_id=vid FOR UPDATE;
 IF NOT FOUND THEN RETURN NULL; END IF;
 IF TG_OP='DELETE' OR NEW.status='deleted' THEN
  UPDATE mpd_video_state SET deleted_at=COALESCE(deleted_at,now()),published=false,published_at=NULL WHERE video_id=vid;
  PERFORM mpd_enqueue_event(vid,'video.deleted',ver,'{}',vid::text||':deleted');
  RETURN NULL;
 END IF;
 IF NEW.status='ready' AND (TG_OP='INSERT' OR OLD.status IS DISTINCT FROM 'ready' OR NEW.media_version<>OLD.media_version) THEN
  PERFORM mpd_enqueue_event(vid,'video.ready',ver,'{}',vid::text||':ready:'||ver);
 END IF;
 IF NEW.transcript_status='ready' AND NEW.transcript_key IS NOT NULL AND
   (TG_OP='INSERT' OR NEW.transcript_key IS DISTINCT FROM OLD.transcript_key) THEN
  IF NEW.transcript_published_generation<>NEW.transcript_generation OR NEW.transcript_generation<1 THEN
   RAISE EXCEPTION 'transcript publication requires current reserved generation';
  END IF;
  UPDATE mpd_video_state SET transcript_version=NEW.transcript_generation WHERE video_id=vid;
  PERFORM mpd_enqueue_event(vid,'video.transcript_ready',ver,jsonb_build_object('transcriptVersion',NEW.transcript_generation),vid::text||':transcript:'||ver||':'||NEW.transcript_generation);
 END IF;
 RETURN NULL;
END $$;
CREATE TRIGGER mpd_video_after_trigger AFTER INSERT OR UPDATE OR DELETE ON videos FOR EACH ROW EXECUTE FUNCTION mpd_video_after();

-- Retained evidence survives account cleanup, retries and deployment rollback.
CREATE FUNCTION mpd_guard_event_evidence() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN
  IF OLD.state <> 'acknowledged' OR OLD.acknowledged_at > now()-interval '30 days' OR OLD.acknowledged_at IS NULL THEN
   RAISE EXCEPTION 'MPD evidence must be retained';
  END IF;
  RETURN OLD;
 END IF;
 IF (NEW.event_id,NEW.video_id,NEW.event_type,NEW.media_version,NEW.dedup_key,NEW.body,NEW.created_at)
 IS DISTINCT FROM (OLD.event_id,OLD.video_id,OLD.event_type,OLD.media_version,OLD.dedup_key,OLD.body,OLD.created_at) THEN
  RAISE EXCEPTION 'MPD event evidence is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER mpd_event_evidence_guard BEFORE UPDATE OR DELETE ON mpd_event_outbox FOR EACH ROW EXECUTE FUNCTION mpd_guard_event_evidence();
