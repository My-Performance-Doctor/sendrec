## 1. Patient summary

- [x] 1.1 Add the summary columns to `mpd_video_state` in a migration.
- [x] 1.2 Add `PUT /api/integrations/mpd/videos/{videoId}/patient-summary` with validation, the work fence and idempotent receipts; test it.
- [x] 1.3 Show the stored summary on a managed watch page for the current media version only, and never SendRec's own summary or chapters there; test it.

## 2. Brand

- [x] 2.1 Serve TT Neoris from `BRAND_FONT_DIR` under `/fonts/brand/` and name it first in managed page headings; test it.
- [x] 2.2 Replace the managed watch page's SendRec credit with a source link; test it.
- [x] 2.3 Set `BRAND_FONT_DIR` in the stack and document adding the font as a private base layer.
- [ ] 2.4 Add a TT Neoris file MPD holds a web licence for to the base image and pin its digest. The only copy found, in Staff OS, is a trial build.

## 3. Docs and verification

- [x] 3.1 Document the summary route, the font and the footer in `docs/mpd/README.md` and the infrastructure README.
- [x] 3.2 Run the Go, web and infrastructure checks.
- [x] 3.3 Render the managed pages at phone size and capture them for the pull request. Synthetic before and after captures are attached to PR #28.
