## Why

Staff OS now plays SendRec recordings in a patient's Videos by framing `/mpd-preview`, as the Staff OS handover describes. The page cannot style itself. Its Content Security Policy allows styles only from SendRec's origin or with the request's nonce, so the `style` attribute that sizes the player is ignored. Framed at 16:9, the page shows its heading and status in default black text, then the recording at its own size, cut off on the right and at the bottom with its controls out of view. SendRec's own detail page frames the preview the same way, and the 26 October evidence screenshot shows the same crop.

## What Changes

- `/mpd-preview` carries its styles in a `<style>` element with the request's CSP nonce, and drops the blocked `style` attribute.
- The player fills the page and letterboxes the recording on black, whether framed or opened in a tab.
- The heading is visually hidden. The status shows on its own while access is checked or after it ends. While media is loaded it is visually hidden but still read by screen readers.
- The handoff, redemption, media renewal, playback observations and framing policy are unchanged.

## Capabilities

### New Capabilities

- `mpd-preview-frame`: the private staff preview fits the frame or tab that shows it.

### Modified Capabilities

None.

## Impact

`internal/video/mpd_player.go` and a test. No API, database, script or setting changes. SendRec's own detail page gets a preview that fits too. Staff OS transcript times still open the watch page; letting Staff OS move the framed preview to a time by message is a possible follow-up, not part of this change.

## Approval and delivery

Josh asked on 2026-11-05 for staff to play videos inside Staff OS. As with MPD-258, the change is applied with this proposal so it can be checked in a browser. `CLAUDE.md` asks for Sami's review before apply, so the pull request stays unmerged until Sami has reviewed this proposal, and a human merges it.
