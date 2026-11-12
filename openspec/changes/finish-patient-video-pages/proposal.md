## Why

Patients now open MPD's pages, but three things still differ from MPD's own apps. Staff OS shows each video's patient summary and its timed points; the patient sees only the transcript. Staff OS headings use TT Neoris; the patient pages use Geist. The watch page's footer still credits SendRec and links to sendrec.eu.

## What Changes

- mpd-api can set or clear a managed recording's patient summary through the integration API: a short text and up to eight timed points. It sends only a summary staff have approved. Each summary belongs to the media version it was written for, so new media hides it until mpd-api sends another.
- A managed recording's watch page shows that summary above the transcript, with the points under "In this video". Tapping a point plays the video from there. SendRec's own AI summary and chapters never show on a managed page.
- Managed pages set headings in TT Neoris when the runtime image carries the font, and fall back to Geist when it does not. The font stays out of this public repository: MPD's private base image adds it, and the server serves it from a fixed directory.
- The managed watch page's footer drops the SendRec credit and links to this repository's source instead, which keeps the AGPL offer of source on the page patients use.
- Unmanaged shares are unchanged.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `patient-video-pages`: adds the approved summary, TT Neoris headings and the source link. The capability comes from `brand-patient-video-pages`, which is not archived yet, so its requirements here are added ones.

## Impact

A migration adding the summary columns to `mpd_video_state`, a new adapter route, the watch page handler and template, `internal/video/mpd_pages.go`, a font route, the CDK environment, the base image digest, `docs/mpd/README.md` and the infrastructure README. mpd-api approval and sending and the Staff OS approval step ship in their own repositories against this contract.

## Approval and delivery

Sami asked for this on 2026-11-12 and said to execute it. A human merges the pull request.
