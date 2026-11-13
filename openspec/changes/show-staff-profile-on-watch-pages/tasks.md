## 1. Staff profile from the grant

- [x] 1.1 Accept an optional `profile` in the access grant. Drop a bad name or a profile with a bad photo version, without refusing access; test it.
- [x] 1.2 Add `mpd_staff_photos` in a migration.
- [x] 1.3 Apply the profile on sign-in and on each grant refresh: the name, then the photo only when its version changes; test it against a database.

## 2. Watch page

- [x] 2.1 Show the stored photo inline on a managed watch page, with initials as the fallback; test it.

## 3. Docs and verification

- [x] 3.1 Document the profile and the photo route in `docs/mpd/README.md` and `docs/mpd/contracts.json`.
- [x] 3.2 Run the Go checks.
- [x] 3.3 Render the managed watch page at phone width with synthetic data for the pull request.
- [ ] 3.4 After the mpd-api change deploys, sign in as a synthetic staff member on staging and confirm that their name and photo show on a managed share.
