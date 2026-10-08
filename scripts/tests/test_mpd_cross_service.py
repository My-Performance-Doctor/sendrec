"""Run from mpd-api's Poetry environment against two disposable loopback DBs.

MPD_API_CHECKOUT=/path/to/mpd-api/application SENDREC_TEST_PG_DSN=... \
TEST_DATABASE_URL=postgres://... poetry run pytest /path/to/sendrec/scripts/tests/test_mpd_cross_service.py -v

Go's real adapter and outbox exchange HTTP with mpd-api's real webhook/adapter.
Only media bytes, transcript enrichment and outbound task delivery are faked.
"""

import importlib.util
import json
import os
from pathlib import Path
import socket
import subprocess
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest.mock import Mock
from uuid import UUID, uuid4

import pytest
import requests
from fastapi import FastAPI, Request, Response
from fastapi.testclient import TestClient
from pydantic import SecretStr

if not os.environ.get("MPD_API_CHECKOUT"):
    pytest.skip(
        "MPD_API_CHECKOUT is required for the optional cross-service drill",
        allow_module_level=True,
    )
API = Path(os.environ["MPD_API_CHECKOUT"]).resolve()
# Load the consumer's existing synthetic configuration without touching its checkout.
config_spec = importlib.util.spec_from_file_location(
    "consumer_test_config", API / "tests/conftest.py"
)
config_fixture = importlib.util.module_from_spec(config_spec)
config_spec.loader.exec_module(config_fixture)
config_fixture.pytest_configure(None)
REPO = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location(
    "cross_service_fixtures", API / "tests/video_report/sendrec/conftest.py"
)
fixtures = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixtures)
db = fixtures.db


def test_actual_provider_and_consumer(db, monkeypatch, tmp_path):
    from config import settings
    from api.principal import Principal, PrincipalKind
    from api_client.permissions import ALL_PERMISSIONS
    from video_report.sendrec import routes
    from video_report.sendrec.adapter import SendRecAdapter
    from video_report.sendrec.processing import process_recording
    from video_report.sendrec.repository import RecordingRepository

    monkeypatch.setattr(routes, "pooled_connection", db.connection)
    monkeypatch.setattr(settings, "sendrec_service_token", SecretStr("s" * 32))
    app = FastAPI()
    app.include_router(routes.webhook_router)
    client = TestClient(app)
    evidence = {"attempts": 0, "lost_response": False}

    class Receiver(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def do_POST(self):
            raw = self.rfile.read(int(self.headers["Content-Length"]))
            result = client.post(
                "/api/video-reports/sendrec/webhook",
                content=raw,
                headers=dict(self.headers),
            )
            evidence["attempts"] += 1
            if result.status_code == 202 and not evidence["lost_response"]:
                evidence["lost_response"] = True
                self.connection.shutdown(socket.SHUT_RDWR)
                self.connection.close()
                return
            self.send_response(result.status_code)
            self.end_headers()
            self.wfile.write(result.content)

    receiver = ThreadingHTTPServer(("127.0.0.1", 0), Receiver)
    thread = threading.Thread(target=receiver.serve_forever, daemon=True)
    thread.start()
    output = tmp_path / "provider.json"
    log = tmp_path / "provider.log"
    container_name = "sendrec-cross-service-" + uuid4().hex[:12]
    command = [
        "docker",
        "run",
        "--rm",
        "--name",
        container_name,
        "--network",
        "host",
        "-v",
        f"{REPO}:/src",
        "-v",
        f"{tmp_path}:/evidence",
        "-w",
        "/src",
        "-v",
        "sendrec-go-mod:/go/pkg/mod",
        "-v",
        "sendrec-go-cache:/root/.cache/go-build",
    ]
    env = {
        "TEST_DATABASE_URL": os.environ["TEST_DATABASE_URL"],
        "SENDREC_CROSS_SERVICE_OUTPUT": "/evidence/provider.json",
        "SENDREC_CROSS_SERVICE_RECEIVER": f"http://127.0.0.1:{receiver.server_port}/events",
        "SENDREC_CROSS_SERVICE_TENANT": str(db.ids.tenant),
        "SENDREC_CROSS_SERVICE_STAFF": str(db.ids.owner),
    }
    for key, value in env.items():
        command += ["-e", f"{key}={value}"]
    command += [
        "golang:1.26.6-alpine",
        "go",
        "test",
        "./internal/video",
        "-run",
        "^TestMPDCrossServiceHarness$",
        "-count=1",
        "-v",
    ]
    stream = log.open("w")
    process = subprocess.Popen(command, stdout=stream, stderr=subprocess.STDOUT)
    origin = None

    def control(operation):
        response = requests.post(
            f"{origin}/control/{operation}",
            headers={"Authorization": "Bearer synthetic-control"},
            timeout=90,
        )
        assert response.status_code == 204, response.text

    def drain():
        while True:
            with db.connection() as conn:
                work = RecordingRepository(conn).lease("video_provider_inbox")
            if not work:
                return
            with db.connection() as conn:
                RecordingRepository(conn).apply(work)

    def row():
        with db.connection() as conn:
            return RecordingRepository(conn).one(
                "SELECT * FROM video_provider_recording WHERE video_id=%s",
                (video,),
            )

    def process_recording_once():
        with db.connection() as conn:
            work = RecordingRepository(conn).lease("video_provider_recording")
        assert work
        transcripts = Mock()
        transcripts.reformat_transcript.side_effect = lambda text: text
        transcripts.process_text.side_effect = lambda text: {
            "transcript": text,
            "topic": "Synthetic",
            "summary": "Synthetic",
            "tasks": [{"task": "Synthetic task"}],
        }
        process_recording(
            work,
            SendRecAdapter(work_version=work["work_version"]),
            transcripts,
            db.connection,
        )

    try:
        deadline = time.monotonic() + 300
        while not output.exists():
            assert process.poll() is None, log.read_text()
            assert time.monotonic() < deadline, log.read_text()
            time.sleep(0.2)
        info = json.loads(output.read_text())
        origin, video = info["origin"], UUID(info["videoId"])
        monkeypatch.setattr(settings, "sendrec_origin", origin)
        control("deliver")
        control("deliver")
        drain()
        assert evidence["lost_response"] and evidence["attempts"] == 3
        assert row()["state"] == "unclaimed"
        with db.connection() as conn:
            assert (
                conn.execute("SELECT count(*) FROM video_provider_inbox").fetchone()[0]
                == 2
            )
            principal = Principal(
                PrincipalKind.STAFF,
                str(db.ids.owner),
                db.ids.tenant,
                ALL_PERMISSIONS,
                "Synthetic",
            )
            routes.claim(
                row()["id"],
                routes.ClaimRequest(patientId=db.ids.patient),
                Request(
                    {
                        "type": "http",
                        "path": "/api/v1/video-recordings/claim",
                        "headers": [],
                    }
                ),
                Response(),
                principal,
                conn,
                Mock(),
            )
        process_recording_once()
        with db.connection() as conn:
            assert conn.execute("SELECT count(*) FROM video_report").fetchone()[0] == 1
            assert (
                conn.execute("SELECT count(*) FROM patient_timeline").fetchone()[0] == 1
            )
            assert (
                conn.execute("SELECT count(*) FROM video_provider_effect").fetchone()[0]
                == 1
            )
            assert conn.execute(
                "SELECT viewed_by_user,available FROM video_report"
            ).fetchone() == (False, True)
        control("preview")
        control("deliver")
        drain()
        with db.connection() as conn:
            assert (
                conn.execute("SELECT viewed_by_user FROM video_report").fetchone()[0]
                is False
            )
        control("anonymous")
        control("deliver")
        drain()
        with db.connection() as conn:
            assert (
                conn.execute("SELECT viewed_by_user FROM video_report").fetchone()[0]
                is True
            )
        control("transcript")
        control("deliver")
        drain()
        process_recording_once()
        with db.connection() as conn:
            assert conn.execute("SELECT count(*) FROM video_report").fetchone()[0] == 1
            assert (
                conn.execute("SELECT transcript FROM video_report").fetchone()[0]
                == "Synthetic updated caption"
            )
            assert (
                conn.execute("SELECT count(*) FROM patient_timeline").fetchone()[0] == 1
            )
        control("delete")
        control("deliver")
        drain()
        assert row()["state"] == "deleted"
        with db.connection() as conn:
            assert conn.execute(
                "SELECT available,video_url,video_link FROM video_report"
            ).fetchone() == (False, None, None)
            assert (
                conn.execute("SELECT count(*) FROM patient_timeline").fetchone()[0] == 1
            )
        print("Cross-service evidence:", json.dumps(evidence, sort_keys=True))
    finally:
        if origin:
            control("stop")
        try:
            process.wait(timeout=30)
        except subprocess.TimeoutExpired:
            subprocess.run(
                ["docker", "rm", "-f", container_name],
                check=False,
                capture_output=True,
                timeout=30,
            )
            process.wait(timeout=10)
        stream.close()
        receiver.shutdown()
        receiver.server_close()
        thread.join(timeout=5)
    assert process.returncode == 0, log.read_text()
