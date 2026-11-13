## 1. Coverage and the event

- [x] 1.1 Add migration 000078 and `mpdevents.Played`; verify with Postgres tests for 80%, seeks, rewatches, pace, positions past the end, previews, session life, a session that never started and an unknown length.
- [x] 1.2 Add the progress route and player reports; verify the route's rate limit and access test, and the producer fixture test.

## 2. Close out

- [ ] 2.1 Merge after Window B (MPD-482), deploy through the promote workflow, and play a staging test video to 80% to see `watchedAt` in mpd-api.
