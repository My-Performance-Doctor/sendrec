## 1. Import

- [x] 1.1 Add `mpd_vimeo_imports` (000085).
- [x] 1.2 Add the import route with its own tokens: check the file, create the managed video on its recording date, start its thumbnail and leave the encode to the normalize sweep, no transcript, idempotent on the Vimeo id.
- [x] 1.3 Test creation, reruns, refusals and the off switch against PostgreSQL 18.

## 2. Configuration

- [x] 2.1 Read `MPD_IMPORT_TOKEN_HASHES` and `MPD_IMPORT_OWNER_USER_ID`; add the optional `mpdImportOwnerUserId` deploy setting in CDK, with a test.
