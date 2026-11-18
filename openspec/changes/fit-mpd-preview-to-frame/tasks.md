## 1. Fit the preview

- [x] 1.1 Serve the `/mpd-preview` styles in a `<style>` element with the request's nonce: the player fills the page and letterboxes on black, the heading is visually hidden, and the status shows only while no media is loaded. Test the nonce and the absence of style attributes, and check the page framed at 560 by 315 in Chromium under the page's CSP.
- [x] 1.2 Name the recording in a header when the preview is opened in its own tab (MPD-515): title, recording date and patient link state from the redeem; header cleared when access ends; title kept out of the tab title.
- [x] 1.3 Act on the Review desk's findings on #43: no "yet" for an unpublished link, the link state refreshed on each media renewal, the date written by the server in the clinic's zone (Australia/Sydney), a 2 px focus ring on the player, "This preview has closed. Open it again from Staff OS." after a refresh, a full-title toggle, a visible "Staff preview" label, and a redeem test with an expired, email-gated link. Check in Chromium at 900, 1280, 1440 and 1920 px.

## 2. Staging

- [ ] 2.1 After merge and promotion, open a synthetic recording from Staff OS staging Videos and confirm the preview fills the frame with its controls in view, in Chrome, Safari and Firefox.
- [ ] 2.2 After the next release, open a sent video from Staff OS in a new tab and confirm the header names it.
