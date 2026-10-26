# Staff OS handover

This guide is for building recording into Staff OS's results workflow on top of SendRec. SendRec stores, processes and serves the video. mpd-api links a recording to a patient and creates the report and timeline entry. Staff OS owns the screens. The reference spec is [README.md](README.md). This page is the order of calls and the things that trip people up.

[`scripts/mpd-staff-client-example.py`](../../scripts/mpd-staff-client-example.py) makes every call below against a real deployment. It passed against staging on 2026-10-13, up to and including the claim call.

## Before you start

SendRec only accepts an approved client from an exact origin. Send these to whoever deploys SendRec:

| Needed | Why | SendRec setting |
|---|---|---|
| Staff OS origin for each environment, such as `https://staff.example.com` | CORS for the API and the bucket, and framing for the preview | `MPD_ALLOWED_ORIGINS` |
| Local development origin, such as `https://localhost:5173` | Same, staging only. It must be https, so run the dev server with HTTPS. Production deploys reject `localhost` and IP address origins | `MPD_ALLOWED_ORIGINS` |
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
   | list unclaimed, claim to patient ------------------------------------------>|
   |                           |<- password + publish (service call) -----------| report, timeline
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
- A 403 `origin is not allowed` means your origin is missing from `MPD_ALLOWED_ORIGINS`. A 401 means denied: unknown client, expired token, no video access, or no enabled workspace. A 503 means mpd-api, Cognito or the database is unreachable. A 409 means either an existing SendRec account has the same email and needs an operator link, or the workspace's seat limit is reached. The exchange, hosted sign-in, handoff and origin check log the reason on the SendRec side as `mpd sign-in refused`, with `entry`, `stage` and `outcome` fields. A provisioning refusal also logs a `mpd provisioning refused` line with its reason just before it. Session renewal for SendRec's own pages does not log reasons yet.

### 2. Record and upload

Record in the browser with `MediaRecorder`. Chrome and Firefox produce WebM; SendRec converts every recording to MP4, so Safari and the patient app can play it.
A take whose frame size changes part way through, such as one where the doctor went full screen, is converted at its first frame's size with the later section letterboxed. Chrome's `avc3` MP4 recordings are converted for the same reason.

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

Poll `GET /api/videos/{id}/mpd` until `metadata.status` is `ready`, a few seconds apart. Every `/api/videos` call, including this one, the preview and the upload-link renewal, shares one rate limit of 2 requests a second with bursts of 10 for each address SendRec sees, so clients behind one address share it. A 429 carries `Retry-After`; wait that long and retry. On staging a 10-second clip took about 5 seconds to transcode and about 70 seconds to transcribe. The transcript arrives after `ready`, so don't block the screen on it.

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

### 5. Claim to a patient in mpd-api

The patient link comes from the claim, not from Staff OS. mpd-api learns about each recording from SendRec's `video.ready` event, usually within seconds. These calls go to mpd-api with the Staff OS Cognito token, not the SendRec token. Fields are camelCase.

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

A 202 means accepted, and the body carries the recording's mpd-api `id` and `state`. mpd-api then does the rest in the background: it makes the recording private if anything published it, sets the link's password from the patient's own password policy, publishes through SendRec's service API, and creates the report and timeline entry. Staff OS never calls SendRec's password or publication endpoints for a claimed recording.

Read progress with `GET /api/v1/video-recordings/{id}`. `state` moves through `pending`, `running` and sometimes `waiting_captions`, then `done` with `reportId` set. A `failureCode` beside `pending` or `running` is a failed attempt; mpd-api backs off and tries again, and the code stays on the recording, including through `waiting_captions`, until an attempt succeeds. `done` clears it. After twelve attempts the state becomes `dead` with the last code. A dead claim restarts on its own when a newer transcript arrives or the media changes, which also resets the attempt count; otherwise an operator replays it. The link and its password live on the report, through mpd-api's existing routes `GET /api/users/{patientId}/video-reports/{reportId}` and `.../password`.

A 409 means the recording is unavailable or belongs to another patient. Claiming the same recording to the same patient again is safe.

Listing needs the `VIDEO_LIST` permission, reading a recording or its report needs `VIDEO_READ`, claiming needs `VIDEO_WRITE`, and the report password needs `VIDEO_PASSWORD`.

### Sharing outside the results workflow

SendRec can also publish a recording on its own, for a link that belongs to no patient. Set a password with `PUT {SENDREC}/api/videos/{id}/password` and `{"password": "..."}`, then `PUT {SENDREC}/api/videos/{id}/publication` with `{"published": true, "mediaVersion": {from metadata}}`. Publishing without a password or before processing finishes returns 409. A stale `mediaVersion` returns 409 too; re-read the metadata and send the current version. Removing the password unpublishes, `metadata.watchUrl` and `metadata.embedUrl` are set only while published, and an unpublished link returns 404. Do not do this to a recording that will be claimed: mpd-api makes it private again and republishes it under the patient's password policy.

## What Staff OS should not do

- Store SendRec or Cognito tokens anywhere but memory.
- Put a patient identifier in any SendRec request. Patient association lives only in mpd-api.
- Build its own public link or player for unpublished recordings.
- Set a password on, or publish, a recording that will be claimed. mpd-api owns the patient link.
- Retry a failed upload by creating a new recording. Renew the upload link instead, or the take is duplicated.

## Status on 2026-10-13

- Staging SendRec has the integration on, with a synthetic workspace and staff account. Sign-in, recording, upload, processing, transcript, preview, publishing and offboarding all passed against staging.
- Since 2026-10-13 staging signs staff in through mpd-api's `mpd-staff-sendrec` client, and SendRec's own hosted sign-in passed end to end. The token exchange also still accepts mpd-api's staff EHR client, so a Staff OS build on that client only needs its origin added.
- Staging accepts the placeholder origin `https://staff.example.test` and, since 2026-10-22, Staff OS staging's `https://app-staging.mpd.health`, for the API and for direct uploads to the recordings bucket. Send the values in [Before you start](#before-you-start) for any other origin.
- Events reach mpd-api, recordings appear in the unclaimed list, and a claim is accepted and processed up to publication. On staging the report build then fails for two reasons on the mpd-api side, both with its owner. For a patient whose link password flag is off, mpd-api publishes without a password, which SendRec's pilot policy refuses with 409. For a patient with the flag on, staging's `VIDEO_REPORT_PASSWORD_KEY` is not a valid Fernet key, so mpd-api fails before it asks SendRec to change anything. Either way the claim retries with backoff and is `dead` after twelve attempts. Until both are fixed, Staff OS can build and test everything through the claim call, and should expect a `failureCode` after it.
- No browser has been tested against this flow yet. Test Chrome, Safari and Firefox, especially Safari playback of WebM recordings after conversion.
