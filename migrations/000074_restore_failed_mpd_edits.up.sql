ALTER TABLE mpd_video_state ADD COLUMN edit_snapshot JSONB;

CREATE OR REPLACE FUNCTION mpd_video_before() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE ident mpd_external_identities; managed BOOLEAN; snapshot JSONB;
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
  -- Reserve requested work, retaining only the state needed if content survives.
  UPDATE mpd_video_state SET edit_snapshot=jsonb_build_object(
   'mediaVersion',OLD.media_version+1,'fileKey',OLD.file_key,
   'transcriptKey',OLD.transcript_key,'transcriptStatus',OLD.transcript_status,
   'published',published,'publishedAt',published_at,'password',OLD.share_password,
   'workVersion',work_version) WHERE video_id=OLD.id;
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
 IF OLD.status='processing' AND NEW.status='ready' THEN
  SELECT edit_snapshot INTO snapshot FROM mpd_video_state WHERE video_id=NEW.id FOR UPDATE;
  IF snapshot IS NOT NULL AND (snapshot->>'mediaVersion')::integer=NEW.media_version
     AND snapshot->>'fileKey'=NEW.file_key THEN
   -- Failure/recovery retained the original object. Publish its existing caption
   -- under the reserved media version without reviving any old worker's claim.
   IF NEW.transcript_generation=0 AND NEW.transcript_key IS NOT DISTINCT FROM snapshot->>'transcriptKey' THEN
    NEW.transcript_status := snapshot->>'transcriptStatus';
    IF NEW.transcript_status='ready' THEN
     NEW.transcript_generation := 1;
     NEW.transcript_published_generation := 1;
    ELSIF NEW.transcript_status='processing' THEN
     NEW.transcript_status := 'pending';
     NEW.transcript_started_at := NULL;
    END IF;
   END IF;
   UPDATE mpd_video_state SET
    published=COALESCE((snapshot->>'published')::boolean,false)
      AND work_version=(snapshot->>'workVersion')::bigint
      AND NEW.share_password IS NOT DISTINCT FROM snapshot->>'password',
    published_at=CASE WHEN COALESCE((snapshot->>'published')::boolean,false)
      AND work_version=(snapshot->>'workVersion')::bigint
      AND NEW.share_password IS NOT DISTINCT FROM snapshot->>'password'
      THEN (snapshot->>'publishedAt')::timestamptz ELSE NULL END
    WHERE video_id=NEW.id;
  END IF;
  UPDATE mpd_video_state SET edit_snapshot=NULL WHERE video_id=NEW.id;
 ELSIF NEW.status='deleted' THEN
  UPDATE mpd_video_state SET edit_snapshot=NULL WHERE video_id=NEW.id;
 END IF;
 IF NEW.transcript_status='pending' AND OLD.transcript_status IS DISTINCT FROM 'pending'
    AND NEW.transcript_generation=OLD.transcript_generation THEN
  NEW.transcript_generation := OLD.transcript_generation+1;
 END IF;
 RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION mpd_video_after() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE vid UUID; ver INTEGER;
BEGIN
 IF TG_OP='DELETE' THEN vid:=OLD.id; ELSE vid:=NEW.id; END IF;
 SELECT media_version INTO ver FROM mpd_video_state WHERE video_id=vid FOR UPDATE;
 IF NOT FOUND THEN RETURN NULL; END IF;
 IF TG_OP='DELETE' OR NEW.status='deleted' THEN
  UPDATE mpd_video_state SET deleted_at=COALESCE(deleted_at,now()),published=false,published_at=NULL,edit_snapshot=NULL WHERE video_id=vid;
  PERFORM mpd_enqueue_event(vid,'video.deleted',ver,'{}',vid::text||':deleted');
  RETURN NULL;
 END IF;
 IF NEW.status='ready' AND (TG_OP='INSERT' OR OLD.status IS DISTINCT FROM 'ready' OR NEW.media_version<>OLD.media_version) THEN
  PERFORM mpd_enqueue_event(vid,'video.ready',ver,'{}',vid::text||':ready:'||ver);
 END IF;
 IF NEW.transcript_status='ready' AND NEW.transcript_key IS NOT NULL AND
   (TG_OP='INSERT' OR NEW.transcript_key IS DISTINCT FROM OLD.transcript_key OR OLD.transcript_status IS DISTINCT FROM 'ready') THEN
  IF NEW.transcript_published_generation<>NEW.transcript_generation OR NEW.transcript_generation<1 THEN
   RAISE EXCEPTION 'transcript publication requires current reserved generation';
  END IF;
  UPDATE mpd_video_state SET transcript_version=NEW.transcript_generation WHERE video_id=vid;
  PERFORM mpd_enqueue_event(vid,'video.transcript_ready',ver,jsonb_build_object('transcriptVersion',NEW.transcript_generation),vid::text||':transcript:'||ver||':'||NEW.transcript_generation);
 END IF;
 RETURN NULL;
END $$;

-- A deliberate policy mutation during processing wins over the saved policy.
-- Updates nested in the video lifecycle trigger reserve/restore state instead.
CREATE FUNCTION mpd_edit_publication_policy() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF pg_trigger_depth()=1 AND NEW.edit_snapshot IS NOT NULL THEN
  NEW.edit_snapshot := jsonb_set(NEW.edit_snapshot,'{published}','false');
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER mpd_edit_publication_policy_trigger BEFORE UPDATE OF published ON mpd_video_state
 FOR EACH ROW EXECUTE FUNCTION mpd_edit_publication_policy();
