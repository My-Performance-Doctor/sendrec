## Why

A managed watch page names the recording's owner under the video. MPD staff sign in with Cognito accounts that have no name attribute, so patients see "MPD staff" with the initials "MS". mpd-api already holds each staff member's name and photo, and Staff OS lets staff edit both.

## What Changes

- mpd-api adds an optional `profile` to `GET /api/v1/staff/video-access`, with `displayName` and `photoVersion`. It serves the photo at `GET /api/v1/staff/video-access/photo` as a 128 px JPEG. That work ships in mpd-api.
- On sign-in and on each grant refresh, SendRec copies `displayName` to the user's name. When `photoVersion` changes, SendRec reads the photo with the same token and stores it. When `photoVersion` is null, SendRec removes the stored photo. A profile without a name keeps the last name.
- A managed watch page shows the photo beside the name. The photo is inline in the page as a data URL, so the page's own access check covers it. Without a photo, the page shows initials, which skip a leading "Dr" or "Prof".
- A bad profile or a failed photo read never blocks sign-in or access. A grant without a profile, from an older mpd-api, changes nothing.
- Unmanaged shares are unchanged.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `mpd-staff-sign-in`: sign-in keeps the staff profile current. The capability comes from `add-mpd-staff-sign-in`, which is not archived yet, so its requirement here is an added one.
- `patient-video-pages`: managed watch pages show the staff photo. The capability comes from `brand-patient-video-pages`, which is not archived yet, so its requirement here is an added one.

## Impact

Migration `000078` adds `mpd_staff_photos`. The change touches `internal/mpd` (grant, sign-in and session refresh), the watch page handler, its template and the managed page CSS, and `docs/mpd`. It adds no configuration: the photo URL is `MPD_ACCESS_URL` plus `/photo`. mpd-api ships its half in its own repository against this contract.

## Approval and delivery

Josh asked for the proposal and the implementation together on 2026-11-13. Sami reviews both in the pull request before it merges. A human merges the pull request.
