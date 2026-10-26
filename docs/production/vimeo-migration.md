# Moving MPD's videos off Vimeo

This is the plan for moving every patient result video from Vimeo into SendRec at Release 1 go-live, so Vimeo can be retired. It is a plan only. Nothing here has run, no Vimeo video has been listed or downloaded, and the import tooling it needs does not exist yet. Each step that touches production runs in a ProductionMaintenance grant window.

The order of record is in Linear MPD-187. Vimeo stays the fallback until the SendRec path passes its checks under MPD-148, and no Vimeo video is deleted until the switched links have run on SendRec through a grace period.

## How a Vimeo video reaches a patient today

Read from mpd-api `main` on 2026-10-26. Paths are in mpd-api.

- Each result video is a `video_report` row with `provider='vimeo'`. `video_id` holds the numeric Vimeo ID, `video_link` the `vimeo.com` page, `video_url` the `player.vimeo.com` embed, and `video_link_password` the Fernet-encrypted link password. The row belongs to the patient through `mpd_user_id`, and the patient timeline holds one `VIDEO_REPORT` entry for it.
- A Vimeo password is set only when the patient's `enable_video_password` flag is on. It is derived from patient details or is eight random characters (`password_helpers.py`). Many older videos may have no password.
- Ingestion runs from Vimeo's transcript webhook through an SQS listener. It reads the MPD ID off the thumbnail, sets the title and password on Vimeo, sends follow-up tasks through Zapier to Asana and saves the report. Staff can also start it by hand.
- Staff read the link and the decrypted password through mpd-api's video-report routes and send them to the patient through their usual channel. mpd-api sends no email or SMS for this.
- mpd-api polls Vimeo play counts every three hours for `provider='vimeo'` rows. A separate Lambda in mpd-automation-maintenance reads an Airtable "Vimeo videos" table and posts watched status, so Airtable holds a second Vimeo-to-patient mapping.
- `VimeoClient.get_user_videos` pages the account's videos, but nothing calls it, and there is no export code.

A SendRec result video instead has a `video_provider_recording` row: a SendRec UUID, the patient, the report, the encrypted password and the publication state. SendRec refuses to publish a managed video without a password (`MPD_REQUIRE_PASSWORD=true`).

## 1. Inventory

Goal: counts, sizes and the match between Vimeo and mpd-api, with no video downloaded.

1. In a grant window, a job in the production account reads the Vimeo token from `mpd-api/vimeo-credentials` at run time and pages `GET /me/videos` with only the fields it needs: URI, created time, duration, size, privacy setting and whether a source download is offered. It writes the listing to a private, encrypted S3 object in the production account. Titles carry patient names, so the listing never leaves that bucket and the report quotes counts only.
2. The same job reads `video_report` rows with `provider='vimeo'` through mpd-api's read-only database role and joins them on the Vimeo ID.
3. It reports these counts:
   - Vimeo videos in total, by year, with total hours and total bytes;
   - videos matched to exactly one report;
   - Vimeo videos with no report, such as test takes, deleted reports or manual uploads;
   - reports whose Vimeo video no longer exists;
   - reports with and without a stored password;
   - videos Vimeo will not offer for download;
   - where the Airtable mapping disagrees with mpd-api.
4. Sami and Josh review the counts and decide what happens to videos with no report: import them unattached for staff review, or leave them out.

The volume line in [the cost estimate](operator-decisions.md#9-cost) changes once these counts exist.

## 2. Bulk download

1. Create a private bucket in the production account for the export, for example `mpd-vimeo-export-production`: encrypted, versioned, all public access blocked and no lifecycle rule. It holds patient media under the same rules as the recordings bucket.
2. A job in the production account fetches each matched video's source file from Vimeo's download link into that bucket. It does not run on a laptop. Downloads run a few at a time and respect Vimeo's rate-limit headers. A manifest in the same bucket records each video's state, so the job can stop and resume without fetching anything twice.
3. Each file is checked on arrival: its size matches Vimeo's, ffprobe reads a video stream whose duration is within a second of Vimeo's, and its SHA256 goes in the manifest. A file that fails is retried, then listed for review.
4. Videos Vimeo will not offer for download are listed for a manual decision. Downloading the transcoded rendition is the fallback, but it loses quality.

## 3. Import into SendRec

SendRec has no import path today. Staff Cognito sessions can create and upload one video at a time, but that would re-run every encode and transcript under a fifteen-minute session and mark each video as a new recording. The import needs its own OpenSpec change in each repository:

- **SendRec:** an operator import that creates a managed video in the MPD workspace from an object already copied into the recordings bucket. It keeps the original recording date, normalises through the existing encoder, and either queues a transcript at low priority or skips it. It is authorised by a dedicated service token scope, not a staff session, and it is idempotent on the Vimeo ID.
- **mpd-api:** a migration command that, for each matched report, creates the `video_provider_recording` row with the existing `report_id` and patient, without a new claim, a new report or a Zapier send. It sets the password on SendRec, publishes, and records the SendRec watch and embed URLs. It also needs the code changes listed under "Switch the links" below.

Matching is by Vimeo ID only: the manifest maps each Vimeo ID to its report ID and patient, read from `video_report`. Nothing is matched by title, date or the thumbnail.

Transcripts: mpd-api already holds each report's transcript text, and Whisper on two tasks would take a long time over hours of video. The recommended default is to import without transcripts and add captions later for videos staff ask for. Decide once the inventory gives total hours.

Passwords:

- Where a report has a stored password, set the same one on SendRec, so patients who know it can still watch.
- Where a report has none, SendRec still requires one. Generate one, store it encrypted as mpd-api does for new claims, and staff send it with the new link when a patient asks.
- Whether to replace every password derived from patient details is a separate decision. It means contacting every patient.

## 4. Check every link and password

Before any link switches, an automated check runs over every imported video:

- the watch page refuses access without the password and with a wrong one, and accepts the right one;
- the media answers a range request with 206, and its duration matches the manifest within a second;
- the embed URL loads under the same password rules;
- captions return a non-empty VTT where a transcript was made;
- the SendRec video belongs to the managed workspace, and the mpd-api row points at the right report and patient.

The check prints counts and failing IDs only. Staff then open a sample, say 20 videos across years, through the screen patients use and confirm each plays to the end. The switch waits until every failure is fixed or explicitly excluded.

## 5. Switch the links

mpd-api changes, in their own PR for Jefferson:

- The migration sets `provider='sendrec'` with the SendRec watch and embed URLs on each migrated report, inside a transaction. Before that, it copies each row's Vimeo values to a backup table, which is what makes the switch reversible.
- The three-hourly Vimeo play-count poll skips migrated rows. SendRec playback sets the viewed state through the recording row.
- The uniqueness constraints on legacy Vimeo and provider identity, the adapter's URL check and schema defaults that assume `provider="vimeo"` are reviewed, so a migrated row passes both.
- Bubble and any other screen that embeds the player is checked for assumptions about Vimeo URLs.

Run the switch in batches by year, newest first. After each batch, run the check from step 4 on that batch.

Links that patients already hold in old messages point at `vimeo.com` and break when Vimeo retires. They keep working through the grace period, and the app and Bubble show the new link from the first batch onward.

## 6. Retire Vimeo

1. From go-live, new recordings go through SendRec only. Turn off Vimeo ingestion: the webhook route, the SQS listener and the manual-process route for Vimeo.
2. Turn off the Vimeo play-count poll and the Airtable "Vimeo videos" Lambda. Leave the Zapier webhook in `mpd-api/zapier-vimeo-webhook-url` alone: SendRec claims send their Asana tasks through it too.
3. Keep the Vimeo account and its videos through a grace period. The recommended default is 60 days from the last batch switching.
4. At the end of the grace period, run the inventory again, reconcile it with the migration manifest, export a final count, and only then delete the Vimeo videos and cancel the plan.

## Fallback

- Until MPD-148's checks pass, nothing switches, and Vimeo stays the source for existing reports.
- Before deletion, a switched batch can be reverted by restoring its rows from the backup table. Vimeo still holds the videos.
- After deletion, the fallback is SendRec's own recovery: versioned recordings with nothing expiring, and 35-day database point-in-time recovery. The export bucket stays until MPD approves a clinical retention policy.

## Open decisions

| Decision | Recommended default |
|---|---|
| Vimeo videos with no report | Import unattached for staff review, unpublished |
| Transcripts for migrated videos | None at import; on request afterwards |
| Passwords derived from patient details | Keep them for migrated videos; new recordings already use generated passwords |
| Grace period before deleting Vimeo videos | 60 days after the last batch switches |
| Who runs the migration | MPD HQ prepares and runs it in grant windows, Jefferson merges the mpd-api change, Josh confirms the Staff OS and Bubble screens |
