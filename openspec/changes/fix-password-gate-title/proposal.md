## Why

The 22 October 2026 security audit found that the password and email-gate pages for a shared recording put the recording's title in the HTML and the browser title before the viewer passes the gate. Titles can carry a patient's name, so anyone holding the link, including a link-preview fetcher, learns it. The password check is also limited only per IP address, so attempts spread across addresses can keep guessing one video's password.

## What Changes

- The watch and embed password and email-gate pages show a generic title until access is granted. The page after the gate is unchanged.
- `POST /api/watch/{shareToken}/verify` locks a video for 15 minutes after 10 wrong passwords in a 15-minute window, on top of the per-IP limiter. While locked it answers 429 without checking the password, so even the right password waits. If the lockout state cannot be read, the request is refused.
- A new table, `share_password_attempts`, holds one row per video with the window start and the failure count.

## Capabilities

### New Capabilities

- `share-gate-protection`: gate pages reveal nothing about the recording, and password guessing is bounded per video.

### Modified Capabilities

None.

## Impact

`internal/video` watch and embed pages and `VerifyWatchPassword`, migration `000075`, `docs/mpd/README.md`, which documents the window, and tests. Viewers who already hold a valid watch cookie are not affected by a lockout. Anyone with the link can lock a video's password form for up to 15 minutes by sending wrong passwords; that is the accepted cost of a per-video limit. Playlist passwords are unchanged.

## Approval and delivery

Sami approved these fixes from the audit report on 2026-10-22 and waived PR review for this change.
