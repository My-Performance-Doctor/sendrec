## Why

Patients open MPD's video links on their phones, and the pages they land on are SendRec's. The password page has no logo and says "password", which reads like a patient's app login. A 16-character code fails if it is typed in lowercase, without its hyphens or with a stray space. The page says "Incorrect password" even when the video is locked after too many tries, so the right code looks wrong. An unavailable managed link answers with raw JSON, and an unknown link sends the patient to sendrec.eu.

## What Changes

- A managed recording's password page asks for a video code in MPD's look. It accepts any case, spacing or missing hyphens, offers a Paste button, finds the code in a whole pasted message and opens the video once the code is complete. Too many tries gets its own message.
- An unpublished, deleted or expired managed link, or an unknown link, shows one MPD page, which says nothing about which it was. Other paths keep their JSON answers.
- A managed recording's watch page takes MPD's light look through the existing custom CSS slot, and its logo no longer links to SendRec's sign-in.
- `BRANDING_DEFAULT_LOGO_URL` gives SendRec's own app MPD's orb and favicon.
- Unmanaged shares keep SendRec's pages and free-form passwords. The footer's SendRec credit stays. Embed and email-gate pages are unchanged.

## Capabilities

### New Capabilities

- `patient-video-pages`: what a patient sees when opening a managed recording's link.

### Modified Capabilities

None.

## Impact

`internal/video` watch page and public access middleware, a new `internal/video/mpd_pages.go`, MPD artwork and Geist in `web/public`, one CDK environment value and `docs/mpd/README.md`. Verification, lockout and cookies are unchanged. TT Neoris stays out because this repository is public, so headings use Geist. A custom domain for the links needs DNS and a certificate and is left for a later change.

## Approval and delivery

The proposal needs Sami's review before merge.
