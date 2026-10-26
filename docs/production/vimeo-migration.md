# Moving MPD's videos off Vimeo

This is the plan for moving every patient result video from Vimeo into SendRec at Release 1 go-live, so Vimeo can be retired. It is a plan only. Nothing here has run, no Vimeo video has been listed or downloaded, and the import tooling it needs does not exist yet. Each step that touches production runs in a ProductionMaintenance grant window.

This repository is public, so this copy covers SendRec's side. How MPD's records link each Vimeo video to its patient and report belongs to mpd-api, as this repository's `CLAUDE.md` requires; that detail, and the mpd-api side of each step, is in MPD's private records with the cutover issue. The order of record is MPD-187. Vimeo stays the fallback until the SendRec path passes its checks under MPD-148, and no Vimeo video is deleted until the switched links have run on SendRec through a grace period.

## 1. Inventory

Goal: counts, sizes and the match between Vimeo and MPD's records, with no video downloaded.

1. In a grant window, a job in the production account lists the Vimeo account's videos with only the fields it needs: URI, created time, duration, size, privacy setting and whether a source download is offered. It reads the Vimeo token from Secrets Manager at run time and writes the listing to a private, encrypted S3 object in the production account. Titles can identify patients, so the listing never leaves that bucket and the report quotes counts only.
2. The same job joins the listing to MPD's records of which patient and report each Vimeo video belongs to.
3. It reports these counts:
   - Vimeo videos in total, by year, with total hours and total bytes;
   - videos matched to exactly one report;
   - Vimeo videos with no report, such as test takes, deleted reports or manual uploads;
   - reports whose Vimeo video no longer exists;
   - reports with and without a link password;
   - videos Vimeo will not offer for download.
4. Sami and Josh review the counts and decide what happens to videos with no report.

The volume line in [the cost estimate](operator-decisions.md#9-cost) changes once these counts exist.

## 2. Bulk download

1. Create a private bucket in the production account for the export: encrypted, versioned, all public access blocked and no lifecycle rule. It holds patient media under the same rules as the recordings bucket.
2. A job in the production account fetches each matched video's source file from Vimeo's download link into that bucket. It does not run on a laptop. Downloads run a few at a time and respect Vimeo's rate-limit headers. A manifest in the same bucket records each video's state, so the job can stop and resume without fetching anything twice.
3. Each file is checked on arrival: its size matches Vimeo's, ffprobe reads a video stream whose duration is within a second of Vimeo's, and its SHA256 goes in the manifest. A file that fails is retried, then listed for review.
4. Videos Vimeo will not offer for download are listed for a manual decision. Downloading the transcoded rendition is the fallback, but it loses quality.

## 3. Import into SendRec

SendRec has no import path today. Staff sessions can create and upload one video at a time, but that would re-run every encode and transcript under a fifteen-minute session and mark each video as a new recording. SendRec needs its own OpenSpec change for an operator import that:

- creates a managed video in the MPD workspace from an object already copied into the recordings bucket;
- keeps the original recording date and normalises through the existing encoder, including the fixed output frame;
- either queues a transcript at low priority or skips it;
- is authorised by a dedicated service token scope, not a staff session;
- is idempotent on the Vimeo ID, so a rerun never creates a second video.

mpd-api needs a matching change that attaches each imported video to its existing report and patient without a new claim or report, sets its link password and publishes it. Matching is by Vimeo ID only, never by title, date or thumbnail.

Transcripts: whole-library transcription on two tasks would take a long time over hours of video. The recommended default is to import without transcripts and add captions later for videos staff ask for. Decide once the inventory gives total hours.

Passwords: SendRec refuses to publish a managed video without one (`MPD_REQUIRE_PASSWORD=true`). Where a video already has a link password, the same one is set on SendRec, so patients who know it can still watch. Where it has none, a new one is generated and staff send it with the new link.

## 4. Check every link and password

Before any link switches, an automated check runs over every imported video:

- the watch page refuses access without the password and with a wrong one, and accepts the right one;
- the media answers a range request with 206, and its duration matches the manifest within a second;
- the embed URL loads under the same password rules;
- captions return a non-empty VTT where a transcript was made;
- the SendRec video belongs to the managed workspace and is attached to the right report.

The check prints counts and failing IDs only. Staff then open a sample, say 20 videos across years, through the screen patients use and confirm each plays to the end. The switch waits until every failure is fixed or explicitly excluded.

## 5. Switch the links

mpd-api switches each migrated report from its Vimeo link to its SendRec link, in batches by year, newest first, keeping a copy of the old values so a batch can be reverted. After each batch, the check from step 4 runs on that batch. Any screen that embeds the player is checked for assumptions about Vimeo URLs.

Links that patients already hold in old messages point at Vimeo and break when Vimeo retires. They keep working through the grace period, and MPD's screens show the new link from the first batch onward.

## 6. Retire Vimeo

1. From go-live, new recordings go through SendRec only, and Vimeo ingestion is turned off.
2. Vimeo play-count polling and other Vimeo-only automations are turned off. Shared task automations that SendRec claims also use stay on.
3. The Vimeo account and its videos stay through a grace period. The recommended default is 60 days from the last batch switching.
4. At the end of the grace period, the inventory runs again and is reconciled with the migration manifest. Only then are the Vimeo videos deleted and the plan cancelled.

## Fallback

- Until MPD-148's checks pass, nothing switches, and Vimeo stays the source for existing reports.
- Before deletion, a switched batch can be reverted from the copy of its old values. Vimeo still holds the videos.
- After deletion, the fallback is SendRec's own recovery: versioned recordings with nothing expiring, and 35-day database point-in-time recovery. The export bucket stays until MPD approves a clinical retention policy.

## Open decisions

| Decision | Recommended default |
|---|---|
| Vimeo videos with no report | Import unattached for staff review, unpublished |
| Transcripts for migrated videos | None at import; on request afterwards |
| Existing link passwords | Keep them for migrated videos |
| Grace period before deleting Vimeo videos | 60 days after the last batch switches |
| Who runs the migration | MPD HQ prepares and runs it in grant windows, Jefferson merges the mpd-api change, Josh confirms the Staff OS screens |
