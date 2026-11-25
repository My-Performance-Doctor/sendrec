## Why

SNDR-1 moves every MPD patient result video off Vimeo into SendRec by 4 December 2026 (`docs/production/vimeo-migration.md`, step 3). SendRec has no import path: staff sessions upload one recording at a time, as a new recording, under a fifteen-minute session. The migration needs an operator import that turns a file already in the recordings bucket into a managed video, once per Vimeo video.

## What Changes

- `POST /api/integrations/mpd/imports`, authorised by its own service tokens (`MPD_IMPORT_TOKEN_HASHES`), separate from the MPD service API's. It is off unless the tokens and the owning user (`MPD_IMPORT_OWNER_USER_ID`) are configured.
- The body names the Vimeo id, the original recording date and the duration. The file must already be at `imports/vimeo/<id>.mp4` in the recordings bucket, as `video/mp4`; otherwise the answer is 409.
- SendRec creates one ready, unpublished managed video owned by the import user, whose MPD staff identity in the workspace the existing trigger binds, so mpd-api hears `video.ready` as for any managed video. The video keeps its recording date, is titled `MPD result video` (nothing from Vimeo, whose titles can identify patients), and goes through the usual encoder and thumbnail. No transcript is queued.
- A new table, `mpd_vimeo_imports`, holds the Vimeo id of each import. A second import of the same id answers 200 with the first video and queues nothing, including when two requests race.
- CDK: the optional deploy setting `mpdImportOwnerUserId` adds the owner and the import token verifiers, from the existing MPD integration secret.

## Impact

- New: `internal/video/mpd_import.go` and its test, migration `000085_mpd_vimeo_imports`.
- Changed: `cmd/sendrec/main.go`, `internal/server/server.go` and `server_mpd.go` (config and route), `MpdIntegrationConfig.java` and its test.
- Nothing changes until a deploy names the owner and the secret holds the verifiers. mpd-api's matching change attaches each imported video to its report and publishes it (mpd-api #647).

## Approval and delivery

Sami's go for SNDR-1 on 25 November 2026, through MPD HQ. Agents merge sendrec after MPD HQ's Review desk passes the pull request.
