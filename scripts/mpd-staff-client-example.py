#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = ["requests>=2.32"]
# ///
"""Reference MPD staff client for SendRec: the calls Staff OS makes, in order.

Signs in with a Cognito staff access token, records a file, waits for processing,
opens a private preview, and optionally publishes and claims it to a patient in
mpd-api. Prints statuses and IDs only, never tokens, passphrases or signed URLs.

    export SENDREC_URL=https://<sendrec-host>
    export MPD_CLIENT_ORIGIN=https://<approved-staff-client-origin>
    export MPD_API_URL=https://<mpd-api-host>          # only for --patient-id
    read -rs MPD_STAFF_ACCESS_TOKEN; export MPD_STAFF_ACCESS_TOKEN
    scripts/mpd-staff-client-example.py recording.webm [--publish] [--patient-id UUID]

MPD_CLIENT_ORIGIN must be listed in SendRec's MPD_ALLOWED_ORIGINS and the token's
client in MPD_ALLOWED_CLIENT_IDS. See docs/mpd/staff-os-handover.md.
"""

import argparse
import getpass
import mimetypes
import os
import secrets
import shutil
import subprocess
import sys
import time
import uuid

import requests

TIMEOUT = 30


def env(name: str) -> str:
    value = os.environ.get(name, "").rstrip("/")
    if not value:
        sys.exit(f"{name} is required")
    return value


def duration_of(path: str) -> int:
    if not shutil.which("ffprobe"):
        sys.exit("pass --duration, or install ffprobe")
    out = subprocess.run(["ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=nw=1:nk=1", path],
                         capture_output=True, text=True, check=True).stdout
    return max(1, round(float(out)))


def fail(step: str, r: requests.Response) -> None:
    # Error bodies are short JSON messages such as {"error":"preview unavailable"}.
    sys.exit(f"{step} failed: HTTP {r.status_code} {r.text[:200]}")


class SendRec:
    def __init__(self, base: str, origin: str, cognito_token: str):
        self.base, self.origin, self.cognito = base, origin, cognito_token
        self.headers: dict[str, str] = {}
        self.expires = 0.0

    def sign_in(self) -> dict:
        """Exchange the Cognito staff token for a 15-minute SendRec session.

        There is no refresh token for cross-origin clients: call this again with a
        current Cognito token before expiresAt or after any 401.
        """
        r = requests.post(f"{self.base}/api/auth/mpd/session", timeout=TIMEOUT,
                          headers={"Origin": self.origin, "Authorization": "Bearer " + self.cognito})
        if r.status_code != 200:
            fail("sign-in", r)
        s = r.json()
        self.headers = {"Origin": self.origin, "Authorization": "Bearer " + s["accessToken"],
                        "X-Organization-Id": s["organizationId"]}
        self.expires = time.time() + 14 * 60
        return s

    def call(self, method: str, path: str, **kw) -> requests.Response:
        if time.time() > self.expires:
            self.sign_in()
        r = requests.request(method, self.base + path, headers=self.headers, timeout=TIMEOUT, **kw)
        if r.status_code == 401:
            # The MPD grant is rechecked every 30 seconds; a 401 means sign in again.
            self.sign_in()
            r = requests.request(method, self.base + path, headers=self.headers, timeout=TIMEOUT, **kw)
        return r


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("file", help="MP4 or WebM recording")
    ap.add_argument("--title", default="Staff client example")
    ap.add_argument("--duration", type=int, help="length in whole seconds; read with ffprobe when omitted")
    ap.add_argument("--publish", action="store_true", help="protect the patient link and publish it")
    ap.add_argument("--patient-id", help="claim the recording to this mpd-api patient")
    a = ap.parse_args()

    token = os.environ.get("MPD_STAFF_ACCESS_TOKEN") or getpass.getpass("Cognito staff access token: ")
    sr = SendRec(env("SENDREC_URL"), env("MPD_CLIENT_ORIGIN"), token)

    s = sr.sign_in()
    print("signed in: capabilities", s["capabilities"])
    if not s["capabilities"].get("record"):
        sys.exit("this staff member cannot record")

    # 1. Create the recording. Managed recordings start unpublished.
    content_type = mimetypes.guess_type(a.file)[0] or "video/webm"
    size = os.path.getsize(a.file)
    r = sr.call("POST", "/api/videos", json={"title": a.title, "duration": a.duration or duration_of(a.file),
                                                  "fileSize": size, "contentType": content_type})
    if r.status_code != 201:
        fail("create", r)
    v = r.json()
    vid = v["id"]
    print("created recording", vid, "managed:", v.get("managed"))

    # 2. Upload the bytes straight to storage with the returned headers.
    #    The link lasts 30 minutes. If it expired, renew it; uploaded=true means
    #    the bytes already arrived and you skip straight to finalize.
    upload_headers = {"Content-Type": content_type, **v.get("uploadHeaders", {})}
    with open(a.file, "rb") as f:
        put = requests.put(v["uploadUrl"], data=f, headers=upload_headers, timeout=600)
    if put.status_code == 403:
        r = sr.call("POST", f"/api/videos/{vid}/upload-url", json={"kind": "screen", "contentType": content_type, "fileSize": size})
        renewed = r.json() if r.ok else fail("renew upload", r)
        if not renewed.get("uploaded"):
            with open(a.file, "rb") as f:
                put = requests.put(renewed["uploadUrl"], data=f, headers=upload_headers, timeout=600)
    if put.status_code not in (200, 412):  # 412: the object already exists, so a retry is safe to finalize
        sys.exit(f"upload failed: HTTP {put.status_code}")
    print("uploaded", size, "bytes")

    # 3. Finalize. Retrying returns the first accepted result.
    r = sr.call("PATCH", f"/api/videos/{vid}", json={"status": "ready"})
    if r.status_code not in (200, 204):
        fail("finalize", r)

    # 4. Wait for processing. Transcripts follow shortly after, in the background.
    for _ in range(60):
        r = sr.call("GET", f"/api/videos/{vid}/mpd")
        meta = r.json().get("metadata", {}) if r.ok else fail("read state", r)
        if meta.get("status") == "ready":
            break
        time.sleep(5)
    else:
        sys.exit("recording did not finish processing within five minutes")
    print("ready: mediaVersion", meta["mediaVersion"], "published", meta["published"])

    # 5. Private staff preview. Open previewUrl in a new tab, or embed it as an
    #    iframe from MPD_CLIENT_ORIGIN (see the handover guide for the
    #    postMessage handoff). The handoff works once and expires in 60 seconds.
    nonce = secrets.token_hex(16)
    r = sr.call("POST", f"/api/videos/{vid}/preview", json={"origin": sr.origin, "nonce": nonce})
    if r.status_code != 201:
        fail("preview", r)
    print("preview link created; it is single use and expires in 60 seconds")

    # 6. Optional: publish behind a passphrase. Publishing without one returns 409.
    if a.publish:
        phrase = getpass.getpass("Choose the patient link passphrase: ")
        r = sr.call("PUT", f"/api/videos/{vid}/password", json={"password": phrase})
        if r.status_code != 204:
            fail("protect link", r)
        r = sr.call("PUT", f"/api/videos/{vid}/publication", json={"published": True, "mediaVersion": meta["mediaVersion"]})
        if r.status_code != 200:
            fail("publish", r)
        # watchUrl and embedUrl appear in the metadata only once published.
        meta = sr.call("GET", f"/api/videos/{vid}/mpd").json()["metadata"]
        print("published: patient link", meta["watchUrl"])

    # 7. Optional: claim to a patient in mpd-api with the same Cognito token.
    #    mpd-api learns about the recording from SendRec's video.ready event, so
    #    it can take a few seconds to appear in the unclaimed list.
    if a.patient_id:
        api = env("MPD_API_URL")
        auth = {"Authorization": "Bearer " + token}
        recording = None
        for _ in range(12):
            r = requests.get(f"{api}/api/v1/video-recordings", params={"status": "unclaimed", "limit": 100}, headers=auth, timeout=TIMEOUT)
            if not r.ok:
                fail("list unclaimed", r)
            recording = next((i for i in r.json()["items"] if i["videoId"] == vid), None)
            if recording:
                break
            time.sleep(5)
        if not recording:
            sys.exit("recording has not reached mpd-api; check SendRec's event outbox")
        r = requests.post(f"{api}/api/v1/video-recordings/{recording['id']}/claim", json={"patientId": a.patient_id},
                          headers={**auth, "Idempotency-Key": str(uuid.uuid4())}, timeout=TIMEOUT)
        if r.status_code != 202:
            fail("claim", r)
        print("claimed: state", r.json()["state"])


if __name__ == "__main__":
    main()
