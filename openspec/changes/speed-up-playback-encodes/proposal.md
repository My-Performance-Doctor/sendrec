## Why

The first production recording (16 November 2026, 15:29 UTC) uploaded in 46 seconds. Its report then took 67 minutes, after a manual re-transcription. Staff OS had already failed the take at 10 minutes.

Production logs show three encodes of the one recording on two 2 vCPU tasks:

| UTC | Event |
| --- | --- |
| 15:30:03 | Finalize starts the normalize on task A. Whisper starts beside it. |
| 15:35:48 | The normalize sweep on task A picks the same video, because it is more than 5 minutes old and not normalized yet. It waits for the encoder slot. |
| 15:35:50 | The sweep on task B picks it too and encodes it in parallel. |
| 15:41:10 | The first normalize publishes. The second one on task A gets the slot and encodes the whole recording again. |
| 15:43, 15:51 | Both duplicates finish and are discarded. |
| 16:00:05 | Whisper is killed at the 30-minute deadline. |

The sweep only checks a video's age, so it repeats every encode that runs past 5 minutes, once per task. Since `fix-transcription-capacity`, a transcription waits while its task encodes, so a duplicate encode now delays the transcript directly. Each duplicate also drops the Doctor's noise-reduction filter.

Every playback encode also forces 60 frames a second (`-r 60`). Staff OS records at 30, so x264 encodes each frame twice. On a 2-minute synthetic screen-share at 1920×748 (relative CPU time on one machine, SSIM against the source):

| Settings | CPU | Size | SSIM |
| --- | --- | --- | --- |
| `-preset fast -r 60` (now) | 36.0 s | 8.06 MB | 0.9970 |
| `-preset fast -fpsmax 60` | 20.8 s | 7.58 MB | 0.9977 |
| `-preset veryfast -fpsmax 60` | 14.5 s | 7.21 MB | 0.9973 |

## What Changes

- A conversion (WebM transcode or MP4 normalize) takes a lease on its video before it downloads anything. Another conversion of the same video, from either task, skips while the lease is live. The lease lasts as long as the encode's own deadline plus a minute, so the sweep still recovers a video whose task died. Failure and success release it.
- Playback encodes (normalize, WebM transcode, MP4 trim and webcam composite) keep the source's frame rate up to 60 (`-fpsmax 60`) and use x264's `veryfast` preset. Profile, level, CRF and the memory bounds are unchanged.

Non-goals:

- A hosted transcription provider. It would make transcripts far faster, but it sends patient audio out of the task and needs a privacy decision.
- Remove-segments, which uses x264's default preset and is not part of the Staff OS flow.
- Handing a running encode back at shutdown. A deploy mid-encode leaves the original recording playing until the lease expires and the sweep runs.

## Capabilities

### New Capabilities

- `encode-capacity`: how many encodes of one video may run, and the playback encode settings.

### Modified Capabilities

None.

## Impact

The change adds migration 78 (`videos.encode_started_at`) and touches `internal/video/encoder_params.go`, `normalize.go`, `transcode.go`, `trim.go`, `composite.go` and their tests. The column is nullable, so tasks on the previous image keep working during a rolling deploy. Production changes only with a reviewed production release, whose promotion runs the migration.

## Approval and delivery

Josh asked on 17 November 2026 why the first production recording failed and for faster uploads and transcription. Sami reviews this proposal and its implementation together in one pull request.
