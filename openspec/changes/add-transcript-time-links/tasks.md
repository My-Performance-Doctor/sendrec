## 1. Start at a time

- [x] 1.1 Start the watch and embed players at the `t` query value in seconds, `m:ss`, `h:mm:ss` or `1h2m3s` form, ignoring a time past the end or one that does not parse; test that both pages carry the script and check it in a browser against a synthetic clip.

## 2. Keyboard times

- [x] 2.1 Render transcript and chapter times as buttons labelled "Play from m:ss" with a visible focus ring; test the markup and check Enter plays from that time in a browser.

## 3. Staging

- [ ] 3.1 After merge and promotion, open a synthetic staging video through its password gate with `?t=` and confirm it starts at that time.
