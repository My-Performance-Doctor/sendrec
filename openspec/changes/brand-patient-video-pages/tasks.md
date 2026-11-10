## 1. Pages

- [x] 1.1 Add the managed video code page, keeping its title and body free of the recording's details; test it.
- [x] 1.2 Add one unavailable page for unpublished, deleted and expired managed links and for unknown links, keeping other paths' JSON; test it.
- [x] 1.3 Give the managed watch page MPD's look through the custom CSS slot and remove its logo link; test it.

## 2. Configuration and docs

- [x] 2.1 Add the artwork and Geist to `web/public`, and set `BRANDING_DEFAULT_LOGO_URL` in the stack.
- [x] 2.2 Document the patient pages in `docs/mpd/README.md`.

## 3. Verification

- [x] 3.1 Check entry, paste, wrong code and lockout on phone-sized WebKit and Chromium under the production content security policy.
- [ ] 3.2 Check the pages on real iOS and Android phones on staging after deployment.
