## 1. Watched share across plays

- [x] 1.1 Add migration 000082 `mpd_video_watch` and update it in `mpdevents.Played`; verify with Postgres tests for two halves, repeated seconds, staff previews, one completion per media version and a new media version, concurrent plays, and a link seeded from an earlier completion.
- [x] 1.2 Send `video.playback_progress` and `watchedShare` only with `MPD_WATCH_SHARE_ENABLED`; verify the 5% steps with the setting on, nothing new with it off, and the setting-on producer fixtures.

## 2. Deploy setting and documents

- [x] 2.1 Add the required CDK context `mpdWatchShare`; verify with `watchShareIsARequiredDeploySetting`.
- [x] 2.2 Update `docs/mpd/README.md`, `docs/mpd/contracts.json`, the producer fixtures, the release guides and DEPLOYMENT.md's switch table.

## 3. Close out

- [ ] 3.1 Release migration-first with `mpdWatchShare=false`, then switch each environment on in its own recorded step once its mpd-api accepts the new event and field.
