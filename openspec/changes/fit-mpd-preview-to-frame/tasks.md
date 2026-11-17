## 1. Fit the preview

- [x] 1.1 Serve the `/mpd-preview` styles in a `<style>` element with the request's nonce: the player fills the page and letterboxes on black, the heading is visually hidden, and the status shows only while no media is loaded. Test the nonce and the absence of style attributes, and check the page framed at 560 by 315 in Chromium under the page's CSP.
- [x] 1.2 Name the recording in a header when the preview is opened in its own tab (MPD-515): title, recording date and patient link state from the redeem; header cleared when access ends; title kept out of the tab title.

## 2. Staging

- [ ] 2.1 After merge and promotion, open a synthetic recording from Staff OS staging Videos and confirm the preview fills the frame with its controls in view, in Chrome, Safari and Firefox.
- [ ] 2.2 After the next release, open a sent video from Staff OS in a new tab and confirm the header names it.
