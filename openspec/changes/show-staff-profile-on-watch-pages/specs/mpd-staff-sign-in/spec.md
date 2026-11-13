## ADDED Requirements

### Requirement: Sign-in keeps the staff profile current
SendRec SHALL copy the staff member's display name and photo from the access grant's optional profile on sign-in and on each grant refresh. SendRec SHALL NOT refuse sign-in or access because of the profile.

#### Scenario: A name and a new photo
- **WHEN** a grant's profile has a display name and a photo version that differs from the stored one
- **THEN** the user's name becomes the display name, and SendRec stores the JPEG that it reads from `MPD_ACCESS_URL` plus `/photo` with the same bearer token

#### Scenario: An unchanged photo
- **WHEN** the profile's photo version equals the stored one
- **THEN** SendRec does not read the photo again

#### Scenario: No photo
- **WHEN** the profile's photo version is null
- **THEN** SendRec removes the stored photo

#### Scenario: No name
- **WHEN** the profile's display name is null
- **THEN** the user keeps their current name

#### Scenario: Older mpd-api
- **WHEN** the grant has no profile
- **THEN** the name and the photo do not change

#### Scenario: A bad name
- **WHEN** the profile's display name is blank, has control characters or is over 100 bytes
- **THEN** access continues, the user keeps their current name, and SendRec still applies the photo

#### Scenario: A bad photo version
- **WHEN** the profile's photo version is not 1 to 64 letters, digits, `-` or `_`
- **THEN** access continues and SendRec ignores the whole profile

#### Scenario: A failed photo read
- **WHEN** the photo read fails, or returns anything other than a JPEG of at most 64 KiB and 512 px a side
- **THEN** access continues, SendRec keeps the stored photo, and the next grant tries again
