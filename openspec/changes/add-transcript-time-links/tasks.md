## 1. Start at a time

- [x] 1.1 Start the watch and embed players at the `t` query value in seconds, `m:ss`, `h:mm:ss` or `1h2m3s` form, ignoring a time past the end or one that does not parse; test that both pages carry the script and check it in a browser against a synthetic clip.

## 2. Keyboard times

- [x] 2.1 Render transcript and chapter times as buttons labelled "Play from m:ss" with a visible focus ring; test the markup and check Enter plays from that time in a browser.

## 3. Staging

- [x] 3.1 After merge and promotion, open a synthetic staging video through its password gate with `?t=` and confirm it starts at that time.
  - 30 October 2026: merged PR #20, revision `3d79c3c8`, was promoted to staging as application definition 15 and migration definition 3. The protected watch link with `?t=7` loaded paused at exactly 7 seconds. Actual Enter keys on the 0:00 and 0:07 transcript buttons sought and started playback. The latter matches the caption cue's fractional start at 7.76 seconds. Only an existing synthetic clip was used; no new media was uploaded and no external send ran. MPD-258 is Done in Plane. Deployment and event evidence is summarized in `infrastructure/DEPLOYMENT.md`.
