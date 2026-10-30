## Why

MPD-258 asks for transcripts and summaries whose lines link to their moment in the video, as Fathom does. SendRec's watch page already lists the transcript with a time on each line, and a click plays from there, but the times are not buttons, so they cannot be reached from the keyboard, and no link can open a video at a moment. Staff OS and MPD's summaries need such a link before they can point at a moment.

## What Changes

- The watch and embed pages read `t` from the query string and start playback there: seconds (`?t=83`), minutes and seconds (`?t=1:23`, `?t=1:02:03`) or units (`?t=1m23s`). A time past the end, or one that does not parse, is ignored. The password page reloads the same address, so the time survives the gate.
- Transcript and chapter times on the watch page are buttons labelled "Play from m:ss", with a visible focus ring, so each moment can be played from the keyboard.

## Capabilities

### New Capabilities

- `watch-time-links`: a watch or embed link can name the moment to start at, and transcript times can be played from the keyboard.

### Modified Capabilities

None.

## Impact

`internal/video` watch and embed pages and the shared player script, and tests. No API, database or setting changes. Staff OS and mpd-api need their own changes to show timed lines and build these links; MPD tracks them as follow-ups of MPD-258. The `/mpd-preview` page clears its address on load and is unchanged here.

## Approval and delivery

MPD HQ assigned MPD-258 on 2026-10-27, and the change was applied with this proposal so it could be checked in a browser. `CLAUDE.md` asks for Sami's review before apply, so the pull request stays unmerged until Sami has reviewed this proposal, and a human merges it.
