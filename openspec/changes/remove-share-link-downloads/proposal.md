## Why

Patients watch their result videos through share links. SendRec turns downloads on for every new recording (`download_enabled` defaults to true), so the patient's watch page shows a Download button, publishes the media URL in its `og:video` tags and JSON-LD `contentUrl`, and `/api/watch/{shareToken}/download` hands out a file link. Staff can turn downloads off one video at a time, but nothing stops a recording being sent with them on. The watch page's attempt to block the browser's "Save video as" menu does not work either: it is an inline `oncontextmenu` attribute, which the page's own Content Security Policy blocks.

## What Changes

- A migration turns downloads off on every recording, makes off the default and adds a check that keeps them off.
- The Downloads toggle leaves the sharing settings, and `PUT /api/videos/{id}/download-enabled` is removed.
- With downloads off, existing code already hides the Download button, `og:video` tags and `contentUrl`, sets `controlsList="nodownload"` on the watch page's video, and answers 403 at the share-link download endpoint.
- The shared player script cancels the video's context menu from nonced script, on the watch, embed and playlist pages. The blocked inline attribute is dropped.
- Signed-in staff can still download from the library and detail page.

This removes every download the pages offer. It is not copy protection: the browser still receives the recording through a short-lived signed URL, so a determined viewer can save it with developer tools or a screen recorder.

## Capabilities

### New Capabilities

- `share-link-downloads`: recordings opened through a share link offer no download.

### Modified Capabilities

None.

## Impact

A new migration, `internal/video` (settings handler, watch page, shared player script), the route in `internal/server`, the OpenAPI spec, the video detail page's sharing settings, and tests. `downloadEnabled` stays in the video API and always reads false. Rolling the migration back drops the check and restores the old default, but cannot restore which recordings had downloads on.

## Approval and delivery

Josh asked on 2026-11-09 to remove download options for patients. As with earlier changes, it is applied with this proposal so it can be checked. `CLAUDE.md` asks for Sami's review before apply, so the pull request stays unmerged until Sami has reviewed this proposal, and a human merges it.
