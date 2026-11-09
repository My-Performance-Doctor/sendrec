## 1. Turn off share-link downloads

- [x] 1.1 Add a migration that turns downloads off on every recording, defaults them off and adds a check that keeps them off. Test against PostgreSQL that a new recording has downloads off and that turning them on is rejected, and run the migration down and up again.
- [ ] 1.2 Remove the Downloads toggle and `PUT /api/videos/{id}/download-enabled`, with its handler, route, OpenAPI entry and tests.
- [ ] 1.3 Cancel the video's context menu in the shared player script and drop the watch page's inline `oncontextmenu` attribute. Test the watch, embed and playlist pages, and check in Chromium under the page's CSP that right-clicking the video opens no menu.

## 2. Staging

- [ ] 2.1 After merge and promotion, open a synthetic recording's watch link in Chrome, Safari and Firefox: no Download button, no "Save video as" on right-click, and `/api/watch/{shareToken}/download` answers 403. Confirm staff can still download it from the library.
