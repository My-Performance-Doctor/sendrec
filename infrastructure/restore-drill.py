#!/usr/bin/env python3
"""Local synthetic PostgreSQL/media restore rehearsal; never contacts AWS.

This is not an RDS PITR or S3 version restore and cannot approve production RPO.
The disposable database container has no network and no host port bindings.
"""
import argparse
import hashlib
import json
import pathlib
import shutil
import subprocess
import tempfile
import time
import uuid

ROOT = pathlib.Path(__file__).resolve().parents[1]


def run(*args, data=None):
    return subprocess.run(args, input=data, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True).stdout


def drill():
    container = 'sendrec-restore-' + uuid.uuid4().hex[:12]
    with tempfile.TemporaryDirectory(prefix='sendrec-synthetic-restore-') as directory:
        folder = pathlib.Path(directory)
        media = folder / 'source-media'
        media.mkdir()
        for version, colour in ((1,'blue'),(2,'green')):
            run('ffmpeg','-hide_banner','-loglevel','error','-f','lavfi','-i',f'color=c={colour}:s=160x90:d=1','-c:v','libx264','-pix_fmt','yuv420p',str(media/f'v{version}.mp4'))
        (media/'captions.vtt').write_text('WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nSynthetic recovery check.\n')
        hashes = {p.name:hashlib.sha256(p.read_bytes()).hexdigest() for p in media.iterdir()}
        run('docker','run','--detach','--network','none','--name',container,'-e','POSTGRES_HOST_AUTH_METHOD=trust','postgres:18-alpine')
        try:
            for _ in range(60):
                try:
                    run('docker','exec',container,'pg_isready','-U','postgres')
                    break
                except subprocess.CalledProcessError:
                    time.sleep(0.25)
            else:
                raise RuntimeError('Synthetic database failed to start')
            def sql(db, statement):
                return run('docker','exec','-i',container,'psql','-v','ON_ERROR_STOP=1','-U','postgres','-d',db,'-At',data=statement.encode()).decode()
            sql('postgres','CREATE DATABASE source; CREATE DATABASE restored;')
            for migration in sorted((ROOT/'migrations').glob('*.up.sql')):
                sql('source',migration.read_text())
            sql('source',"""
BEGIN;
SET LOCAL sendrec.mpd_provision='1';
INSERT INTO users(id,email,password,name) VALUES('10000000-0000-4000-8000-000000000001','restore@example.invalid','disabled','Synthetic Restore');
INSERT INTO organizations(id,name,slug) VALUES('20000000-0000-4000-8000-000000000001','Synthetic Restore','synthetic-restore');
INSERT INTO mpd_managed_workspaces(organization_id,tenant_id,issuer,enabled,seat_limit)
VALUES('20000000-0000-4000-8000-000000000001','30000000-0000-4000-8000-000000000001','https://synthetic.example.invalid',true,2);
INSERT INTO mpd_external_identities(issuer,subject,user_id,organization_id,staff_id,tenant_id)
VALUES('https://synthetic.example.invalid','synthetic-subject','10000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001','40000000-0000-4000-8000-000000000001','30000000-0000-4000-8000-000000000001');
INSERT INTO videos(id,user_id,organization_id,title,status,file_key,share_token,share_password,transcript_key,transcript_status,transcript_generation,transcript_published_generation)
VALUES('50000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001','Synthetic restore','ready','v1.mp4','synthetic-restore',crypt('synthetic-password',gen_salt('bf')),'captions.vtt','ready',1,1);
UPDATE videos SET status='processing' WHERE share_token='synthetic-restore';
UPDATE videos SET status='ready',file_key='v2.mp4',transcript_key='captions.vtt',transcript_status='ready',transcript_generation=1,transcript_published_generation=1 WHERE share_token='synthetic-restore';
COMMIT;
""")
            backup_time=time.monotonic()
            dump=run('docker','exec',container,'pg_dump','-U','postgres','-Fc','source')
            archive=shutil.make_archive(str(folder/'media-backup'),'tar',media)
            recovery_start=time.monotonic()
            run('docker','exec','-i',container,'pg_restore','--exit-on-error','--no-owner','-U','postgres','-d','restored',data=dump)
            restored=folder/'restored-media'
            shutil.unpack_archive(archive,restored)
            assert {p.name:hashlib.sha256(p.read_bytes()).hexdigest() for p in restored.iterdir()} == hashes
            for version in (1,2):
                run('ffmpeg','-hide_banner','-loglevel','error','-i',str(restored/f'v{version}.mp4'),'-f','null','-')
            assert '00:00:00.000 --> 00:00:01.000' in (restored/'captions.vtt').read_text()
            checks=sql('restored',"""SELECT
 (SELECT count(*)=1 FROM mpd_external_identities WHERE subject='synthetic-subject') AND
 (SELECT media_version=2 AND share_password=crypt('synthetic-password',share_password) AND share_password<>crypt('wrong-password',share_password) AND transcript_status='ready' AND file_key='v2.mp4' FROM videos WHERE share_token='synthetic-restore') AND
 (SELECT count(*)>=3 FROM mpd_event_outbox WHERE state='pending');""").strip()
            assert checks=='t', 'Restored identity, media version, protection or outbox differed'
            recovered=sql('restored',"""WITH next AS (SELECT event_id FROM mpd_event_outbox WHERE state='pending' ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1)
UPDATE mpd_event_outbox SET state='leased',lease_token=gen_random_uuid(),lease_until=now()+interval '1 minute' WHERE event_id IN (SELECT event_id FROM next) RETURNING state;""")
            assert 'leased' in recovered
            return {'kind':'local-synthetic-restore','databaseRestored':True,'mediaVersionsDecoded':2,
                    'passwordPositiveAndNegativeControls':True,'captionsRestored':True,'identityRestored':True,
                    'pendingEventClaimed':True,'localSnapshotAgeSeconds':round(recovery_start-backup_time,3),
                    'localRestoreSeconds':round(time.monotonic()-recovery_start,3),
                    'productionRpoVerified':False,'productionRtoVerified':False}
        finally:
            # This invocation created the only container it removes.
            run('docker','rm','--force',container)


if __name__=='__main__':
    parser=argparse.ArgumentParser()
    parser.add_argument('--output',required=True,type=pathlib.Path)
    args=parser.parse_args()
    args.output.write_text(json.dumps(drill(),indent=2)+'\n')
