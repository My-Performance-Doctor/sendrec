# MPD recording integration

SendRec owns staff recording, uploads, media processing and captions. mpd-api owns patient claims, clinical reports, timeline entries and watched status. No patient association belongs in a SendRec request.

The three integration switches default to false. Production preparation starts with zero application tasks. Jeff is reviewing mpd-api PR 211 separately; this implementation does not require or perform its merge. Human review, matching staging configuration and production rollout approval remain separate steps.

## Identity and recording API

Native login starts at `GET /api/auth/mpd/login`. Cognito returns to the fixed callback. The browser redeems the short, one-use `mpd_code` fragment through same-origin `POST /api/auth/mpd/handoff`. State, nonce, PKCE and a browser cookie bind that flow. Bearer credentials never travel in redirect URLs.

A future client exchanges an approved Cognito staff access token through `POST /api/auth/mpd/session`, with its exact configured Origin. The response contains an in-memory SendRec access token. Cross-origin clients receive no refresh cookie. They renew with a current Cognito token. Native renewal uses `/api/auth/refresh`, encrypted server-side Cognito credentials and current MPD authorization.

Sessions enforce the `/api/v1/staff/video-access` response, bounded by its `validUntil` and 30 seconds. Expired grants fail closed during outages. A managed session cannot use personal scope, a different workspace header, old API keys or local password login to expand access. Offboarding retains media.

The grant can carry a `profile` with `displayName` and `photoVersion`. Either can be null. On sign-in and on each grant refresh, SendRec copies the name to the user. When `photoVersion` changes, SendRec reads `GET {MPD_ACCESS_URL}/photo` with the same token. It keeps only a JPEG of at most 64 KiB and 512 px a side. Managed watch pages show that photo beside the name, inline in the page, and show initials without a photo. A null `photoVersion` removes the photo. A null or bad name keeps the last name. A bad `photoVersion` makes SendRec ignore the profile. Neither, nor a failed photo read, blocks access, and the next grant tries the photo again. A grant without a profile, from an older mpd-api, changes nothing.

| Operation | Request | Authorization |
|---|---|---|
| Record | `POST /api/videos`, title, duration, fileSize, contentType and optional webcam size/type | `record` |
| Upload MP4/WebM | `POST /api/videos/upload`, title, fileSize, contentType | `record` |
| Renew pending upload | `POST /api/videos/{id}/upload-url`, kind screen/webcam, fileSize, contentType | Owned pending recording and `record` |
| Finalize | `PATCH /api/videos/{id}`, status ready | Owned pending recording and `record` |
| Read managed state | `GET /api/videos/{id}/mpd` | `read` |
| Private preview | `POST /api/videos/{id}/preview`, exact origin and random nonce | `read` |
| Password | `PUT /api/videos/{id}/password`, existing password request | `manageOwn` or `manageWorkspace` |
| Publish | `PUT /api/videos/{id}/publication`, mediaVersion and published | `manageOwn` or `manageWorkspace` |

Upload bytes go directly to storage. PUT links retain their bounded 30-minute lifetime. Managed uploads require the signed `If-None-Match: *` header, so a retained link cannot replace an existing object. See [AWS conditional-write semantics](https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html). An upload renewal can return `uploaded: true` after checking the stored size and content type. The client then finalizes that recording without another PUT. Finalization retries preserve the first accepted result. Clients renew the camera link after a slow screen upload and retain the take, pending recording ID and upload progress through authorization/network failure. Signing in through another tab then retrying recovers the original take. Only intentional discard deletes it.

Managed recordings start unpublished. The standalone UI offers a private preview and publication controls; it disables copy/share until publication succeeds. The configured password policy applies before publication. Removing a password unpublishes the recording. A media-changing edit advances its version and unpublishes it again. When mpd-api claims a recording it applies the patient's password policy and publishes through the adapter API itself, so a client in the results workflow never publishes.

Preview URLs carry a one-use handoff and nonce in the fragment. The preview redeems them with a same-origin POST, then uses a short video/version/session-bound proof. A future client may open that URL directly or pass its handoff through the exact-origin, nonce-bound embed exchange. The proof grants private preview; the native classification cookie only identifies staff viewing context. Every preview renewal repeats current staff authorization.

## Public access inventory

Persisted media policy remains active when the integration switches or deployment workspace configuration are removed. Rollback must never restore public access to an unpublished recording.

| Registered route | Policy |
|---|---|
| `/{shareToken}`, `/watch/{shareToken}`, `/embed/{shareToken}` | Publication required; existing password/email forms apply, titled "Protected video" instead of the recording title. The short path matches only share token shapes, and generated links use it |
| `/api/watch/{shareToken}` | Publication, password, email and expiry checks |
| `/api/watch/{shareToken}/download` | Never returns a link: share links offer no download, and a database check keeps every recording's downloads off |
| `/api/watch/{shareToken}/thumbnail` | Publication and per-video access |
| `/api/videos/{shareToken}/oembed` | No unpublished or password-protected metadata disclosure |
| Watch `/verify`, `/identify`, `/comments`, `/cta-click`, `/milestone`, `/segments` | Publication required; protected operations require per-video access |
| `/watch/playlist/{shareToken}`, `/embed/playlist/{shareToken}` | Every managed item must pass its own publication/password/email/expiry policy, then playlist policy |
| Playlist `/verify`, `/identify` | No playlist token bypass of a managed item's policy |
| Watch `/renew`, `/playback-session`, `/playback-start` | Publication, password, email and expiry rechecked |

Share password checks are limited per address and per video. After 10 wrong passwords for one video within 15 minutes of the first, `/verify` answers 429 for the rest of that 15-minute window without checking the password, whatever address the request comes from. Viewers who already passed the form keep watching. Anyone holding the link can trigger this lock, so a viewer may have to wait up to 15 minutes.

### Patient pages

A managed recording's link is opened by a patient, usually on a phone, so `internal/video/mpd_pages.go` gives it MPD's pages. The password page asks for a video code. It accepts any case, spacing or missing hyphens, finds a code in a whole pasted message and opens the video once the code is complete. Anything else stays as entered. An unpublished, deleted or expired managed link, or an unknown link, shows one page that does not say which it was. The watch page takes MPD's look through the custom CSS slot. Its footer links to this repository's source in place of SendRec's credit, which keeps the AGPL offer of source on the page patients use. Unmanaged shares keep SendRec's pages, and `MPD_SERVICE_ENABLED` decides the page for an unknown link.

Headings name TT Neoris first and Geist after it. The font's licence keeps it out of this public repository: the server serves `/fonts/brand/tt-neoris-demibold.ttf` from `BRAND_FONT_DIR`, which MPD's private base image is meant to fill, and answers 404 without it, so headings fall back to Geist. The only copy found so far, in Staff OS, is a trial build, so no image carries it yet.

When mpd-api has sent a patient summary for the recording's current media version, the watch page shows it in a Summary tab above the transcript, with its points under "In this video". Each point plays the video from its start time. A managed page never shows SendRec's own AI summary or chapters, and new media hides a summary until mpd-api sends one for it.

Playback/download GET links expire within five minutes. Watch, embed and playlist players renew them and preserve position and paused state. Denied renewal stops playback. Managed library/detail uses the private preview player; download clicks request a fresh authorized link.

## Backend service API

The deployment stores one or two SHA-256 service-token verifiers for rotation. The caller keeps the original high-entropy token in Secrets Manager. A service token can only read metadata/captions or set password, publication or the patient summary for the configured workspace. It cannot act as staff, record, delete or administer workspaces.

`GET /api/integrations/mpd/videos/{videoId}` and `/transcript` return the strict shapes in [contracts.json](contracts.json). Transcript responses include the published media and transcript versions and bounded VTT text.

Both PUT endpoints require `Idempotency-Key` and a positive `X-MPD-Work-Version`. Password bodies contain `mediaVersion` and nullable `password`; publication bodies contain `mediaVersion` and `published`. A database transaction locks the current recording, rejects lower work versions and stale media, applies the mutation and stores its receipt. Reusing an effect key with another body returns 409. A valid replay advances the work fence and returns the original receipt without reapplying the effect. Receipts store keyed request digests and response metadata, never plaintext passwords.

`PUT /api/integrations/mpd/videos/{videoId}/patient-summary` follows the same rules. Its body contains `mediaVersion`, a nullable `summary` of at most 1,200 characters and `points`, at most eight of `{"start": seconds, "text": "..."}` with up to 300 characters each. A null summary with no points clears it. SendRec trims the text, orders the points by start time and stores them with the media version, and answers `{"mediaVersion": n, "patientSummary": true|false}`. Send only a summary staff approved for the patient.

## Events and recovery

The deployment-owned stream emits `video.ready`, `video.transcript_ready`, `video.playback_started` and `video.deleted`. [Contract fixtures](contracts.json) define schemaVersion 1; [producer fixtures](../../internal/mpdevents/testdata/events.json) cover all event classes.

Readiness, transcript publication and deletion commit with their outbox rows. Ownership snapshots survive account changes. Media versions exist before upload; transcript generations are reserved when work is accepted. Late jobs cannot replace newer media or captions. Deletion persists a tombstone before storage purge.

Delivery signs `timestamp + "." + rawBody` with HMAC-SHA256 and sends the timestamp, key ID and signature headers. Retries preserve the event ID and exact body, including after a lost acknowledgement. Leases recover interrupted delivery. Transient retries last 24 hours, exhausted or blocked events remain for replay, and acknowledged evidence remains at least 30 days. Changing user notification settings cannot disable this stream.

A playback session records the first advancing actual play. Page access, preload, stalled playback and seeking alone do not count. Staff previews never set the report watched flag. Signed-out shared-link playback is anonymous evidence, not proof that a named patient watched. Recipient grants are reserved for separate work.

Staff OS builders start with the [handover guide](staff-os-handover.md), which walks through these calls in order. See [verification](verification.md) for local results and [production preparation](../production/README.md) for release, alert and restore requirements. The [operator identity procedure](operator-identity.md) must verify issuer, subject, staff and tenant bindings; matching email alone never transfers an existing account or evaluation media.

## Synthetic client requests

A standalone client first exchanges a current staff token. Replace the placeholders in memory; never save a token in shell history or a request file.

```http
POST /api/auth/mpd/session
Origin: https://staff.example.test
Authorization: Bearer <current-cognito-access-token>
```

Use the returned access token for a recording request:

```http
POST /api/videos/upload
Authorization: Bearer <sendrec-access-token>
Content-Type: application/json

{"title":"Synthetic recording","fileSize":1024,"contentType":"video/mp4"}
```

The response returns the recording ID, direct upload URL and `managed: true`. PUT the exact bytes and content type to that URL with the returned `uploadHeaders`, then call `PATCH /api/videos/{id}` with `{"status":"ready"}`. A 401 requires staff reauthorization; a 403 means the current grant or workspace does not allow the action. Keep the recording ID and local bytes through either failure. A 409 from upload renewal means the recording is no longer pending.

Once processing completes, request `POST /api/videos/{id}/preview` with `{"origin":"https://staff.example.test","nonce":"<random-value-at-least-16-characters>"}`. Open its one-use preview URL. Public watch remains unavailable until an authorized password/publication action succeeds. Preview denial must never fall back to a public URL.

Service mutations return 400 for malformed work/effect identity, 401 for an invalid verifier, 404 outside the configured workspace, and 409 for stale media/work or a conflicting effect key. Retrying the same accepted effect returns its stored response without reapplying it.
