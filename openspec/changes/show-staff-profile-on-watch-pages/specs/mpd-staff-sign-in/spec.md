## ADDED Requirements

### Requirement: Sign-in keeps the staff profile current
SendRec SHALL copy the staff member's display name and photo from the access grant's optional profile on sign-in and when an API request refreshes the grant. SendRec SHALL NOT refuse sign-in or access because of the profile.

#### Scenario: A name and a new photo
- **WHEN** a grant's profile has a display name and a photo version that differs from the stored one
- **THEN** the user's name becomes the display name, and SendRec stores the JPEG that it reads from `MPD_ACCESS_URL` plus `/photo` with the same bearer token, when the response's `ETag` is the quoted photo version

#### Scenario: An unchanged photo
- **WHEN** the profile's photo version equals the stored one
- **THEN** SendRec does not read the photo again

#### Scenario: No photo
- **WHEN** the profile's photo version is null
- **THEN** SendRec removes the stored photo

#### Scenario: No name
- **WHEN** the profile's display name is null, including when its photo version is null too
- **THEN** the user keeps their current name, because mpd-api sends no departure signal

#### Scenario: Offboarded staff
- **WHEN** an operator offboards a staff member
- **THEN** the operator SQL in `docs/mpd/README.md` removes their stored photo and sets their name to "MPD staff"; SendRec infers no departure from a grant

#### Scenario: Older mpd-api
- **WHEN** the grant has no profile
- **THEN** the name and the photo do not change

#### Scenario: A bad name
- **WHEN** the profile's display name is not a string, is blank, has control characters or is over 100 characters
- **THEN** access continues, the user keeps their current name, and SendRec still applies the photo

#### Scenario: A bad photo version
- **WHEN** the profile has no photo version, or one that is neither null nor 1 to 64 letters, digits, `-` or `_`
- **THEN** access continues and SendRec ignores the whole profile

#### Scenario: A slow photo read
- **WHEN** the photo read is slow or hangs
- **THEN** sign-in and the request that refreshed the grant do not wait for it, and only one read per user runs at a time

#### Scenario: A photo removed during its read
- **WHEN** a grant with a null photo version arrives, on any task, while a read of the earlier version is still running
- **THEN** SendRec removes the stored photo at once and does not keep the photo from that read, because a read stores its photo only while the database still names that version as wanted

#### Scenario: A newer photo during a read
- **WHEN** a grant names a newer photo version while a read of an earlier one is still running
- **THEN** the earlier read's photo is not stored and SendRec reads the newer version after it, so it never ends without the newer photo

#### Scenario: Photo metadata
- **WHEN** the photo carries EXIF or other metadata, including an orientation
- **THEN** SendRec turns the pixels to match the orientation and stores a re-encoded JPEG without any metadata

#### Scenario: A failed photo read
- **WHEN** the photo read fails with 401, 403, 408, 429, a 5xx or a network error, returns anything other than a JPEG of at most 64 KiB and 512 px a side, or has an `ETag` other than the quoted photo version
- **THEN** access continues, SendRec keeps the stored photo, and it reads that version again no sooner than five minutes later

#### Scenario: A photo mpd-api does not have
- **WHEN** the photo read answers 400, 404, 410 or 422
- **THEN** access continues, SendRec keeps the stored photo, logs the status once and does not read that version again
