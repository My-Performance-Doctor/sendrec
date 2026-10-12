# Staff OS handover

This guide is for building recording into Staff OS's results workflow on top of SendRec. SendRec stores, processes and serves the video. mpd-api links a recording to a patient and creates the report and timeline entry. Staff OS owns the screens. The reference spec is [README.md](README.md). This page is the order of calls and the things that trip people up.

[`scripts/mpd-staff-client-example.py`](../../scripts/mpd-staff-client-example.py) makes every call below against a real deployment. It passed against staging on 2026-10-12.

## Before you start

SendRec only accepts an approved client from an exact origin. Send these to whoever deploys SendRec:

| Needed | Why | SendRec setting |
|---|---|---|
| Staff OS origin for each environment, such as `https://staff.example.com` | CORS for the API and the bucket, and framing for the preview | `MPD_ALLOWED_ORIGINS` |
| Local development origin, such as `http://localhost:5173` | Same, staging only. Production rejects `localhost` | `MPD_ALLOWED_ORIGINS` |
| Cognito client ID that Staff OS signs staff in with | Only tokens from listed clients are accepted | `MPD_ALLOWED_CLIENT_IDS` |

The staff member also needs a role in mpd-api that grants video access. SendRec asks mpd-api's `/api/v1/staff/video-access` on every sign-in and at least every 30 seconds after.

Both settings are comma-separated and redeploy with SendRec. Changing them needs no code change.

## The flow

```
Staff OS                     SendRec                          S3            mpd-api
   | sign in (Cognito token) ->|-- video-access ------------------------------>|
   |<- session (15 min) -------|
   | create recording -------->|
   |<- upload URL -------------|
   | PUT bytes ----------------------------------------------->|
   | finalize ---------------->|-- transcode, transcribe
   |                           |-- video.ready event --------------------------->| unclaimed list
   | preview (iframe) -------->|
   | password + publish ------>|
   | list unclaimed, claim to patient ------------------------------------------>| report, timeline
```

### 1. Sign in

```http
POST {SENDREC}/api/auth/mpd/session
Origin: {STAFF_OS_ORIGIN}
Authorization: Bearer {cognito access token}
```

The response has `accessToken`, `organizationId`, `capabilities` and `expiresAt`. Send these two headers on every later SendRec call:

```http
Authorization: Bearer {accessToken}
X-Organization-Id: {organizationId}
```

- Keep the token in memory only. Cross-origin clients get no refresh cookie.
- The session lasts 15 minutes. Renew by calling the exchange again with a current Cognito token, before `expiresAt` or after any 401.
- `capabilities` says what to show: `record`, `read`, `manageOwn`, `manageWorkspace` and `readPassword`. Hide what the staff member can't do; the server enforces it anyway.
- A 401 from the exchange means denied: unknown client, wrong origin, no video access, or no enabled workspace. A 503 means mpd-api or Cognito is unreachable. A 409 means either an existing SendRec account has the same email and needs an operator link, or the workspace's seat limit is reached. The reason is logged on the SendRec side as `mpd sign-in refused` with a `stage` field.

### 2. Record and upload

Record in the browser with `MediaRecorder`. Chrome and Firefox produce WebM; SendRec converts every recording to MP4, so Safari and the patient app can play it.

```http
POST {SENDREC}/api/videos
{"title": "...", "duration": 42, "fileSize": 1234567, "contentType": "video/webm"}
```

`duration` is whole seconds, at least 1. Add `webcamFileSize` and `webcamContentType` for a separate camera track. The response has `id`, `uploadUrl`, `uploadHeaders` and `managed: true`.

PUT the bytes to `uploadUrl` with `Content-Type` and every header in `uploadHeaders`. That includes `If-None-Match: *`, which stops a reused link from replacing a recording. A 412 means the bytes are already there.

The link lasts 30 minutes. If it expires mid-upload, call `POST /api/videos/{id}/upload-url` with `{"kind": "screen", "contentType": ..., "fileSize": ...}`. If the response says `uploaded: true`, skip the PUT. Keep the recording ID and the local blob until finalize succeeds, so a network drop or re-sign-in never loses a take.

Then finalize:

```http
PATCH {SENDREC}/api/videos/{id}
{"status": "ready"}
```

Retrying finalize is safe. To throw a take away, `DELETE /api/videos/{id}`.

### 3. Wait for processing

Poll `GET /api/videos/{id}/mpd` until `metadata.status` is `ready`. On staging a 10-second clip took about 5 seconds to transcode and about 70 seconds to transcribe. The transcript arrives after `ready`, so don't block the screen on it.

### 4. Private preview

Recordings start unpublished. Staff watch them through a one-use preview:

```http
POST {SENDREC}/api/videos/{id}/preview
{"origin": "{STAFF_OS_ORIGIN}", "nonce": "{random, 16 to 128 characters}"}
```

The response has `previewUrl`, `handoff` and `nonce`. The handoff works once and expires in 60 seconds. Two ways to show it:

- **New tab.** Open `previewUrl`. It carries the handoff in the URL fragment, which never reaches a server.
- **Embedded.** Frame `{SENDREC}/mpd-preview#nonce={nonce}&parentOrigin={STAFF_OS_ORIGIN}`, without the handoff. The frame posts `{type: "mpd-preview-ready", nonce}` to your origin. Reply with `frame.contentWindow.postMessage({type: "mpd-preview", nonce, handoff}, SENDREC)`. This keeps the handoff out of the frame's URL. Only origins in `MPD_ALLOWED_ORIGINS` can frame the preview.

Each preview renewal rechecks the staff member's access, so a removed staff member loses the preview within about 30 seconds. Never fall back to the public link when preview fails.

### 5. Password and publish

Publishing gives the patient a link. It needs a password first.

```http
PUT {SENDREC}/api/videos/{id}/password
{"password": "..."}

PUT {SENDREC}/api/videos/{id}/publication
{"published": true, "mediaVersion": {from metadata}}
```

- Publishing without a password, or before processing finishes, returns 409.
- `mediaVersion` must match the current version, which protects against publishing a recording that was edited in the meantime. Re-read the metadata after a 409.
- Removing the password unpublishes the recording. So does any edit that changes the media.
- After publishing, `metadata.watchUrl` is the patient link and `metadata.embedUrl` is an embeddable player. Both are null while unpublished, and an unpublished link returns 404.

### 6. Claim to a patient in mpd-api

mpd-api learns about each recording from SendRec's `video.ready` event, usually within seconds. These calls go to mpd-api with the Staff OS Cognito token, not the SendRec token. Fields are camelCase.

```http
GET {MPD_API}/api/v1/video-recordings?status=unclaimed&limit=50
Authorization: Bearer {cognito access token}
```

The response has `items` and `nextCursor`. Match an item to your recording with `videoId`, which is the SendRec recording `id`.

```http
POST {MPD_API}/api/v1/video-recordings/{item.id}/claim
Authorization: Bearer {cognito access token}
Idempotency-Key: {new UUID per claim}
{"patientId": "..."}
```

A 202 means accepted. mpd-api then builds the report and timeline entry in the background; read progress with `GET /api/v1/video-recordings/{id}` and its `state`, `reportId` and `failureCode`. A 409 means the recording is unavailable or belongs to another patient. Claiming the same recording to the same patient again is safe.

Listing needs the `VIDEO_LIST` permission, reading needs `VIDEO_READ`, and claiming needs `VIDEO_WRITE`.

## What Staff OS should not do

- Store SendRec or Cognito tokens anywhere but memory.
- Put a patient identifier in any SendRec request. Patient association lives only in mpd-api.
- Build its own public link or player for unpublished recordings.
- Retry a failed upload by creating a new recording. Renew the upload link instead, or the take is duplicated.

## Status on 2026-10-12

- Staging SendRec has the integration on, with a synthetic workspace and staff account. Sign-in, recording, upload, processing, transcript, preview, password, publishing and offboarding all passed against staging.
- mpd-api's staging receiver and SendRec client wait on mpd-api PR 219. Until it deploys, events queue in SendRec and the claim step can't run on staging.
- Staging currently accepts the placeholder origin `https://staff.example.test` and mpd-api's existing staff EHR client. Send the values in [Before you start](#before-you-start) to get Staff OS added.
- No browser has been tested against this flow yet. Test Chrome, Safari and Firefox, especially Safari playback of WebM recordings after conversion.
