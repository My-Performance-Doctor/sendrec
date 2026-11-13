## ADDED Requirements

### Requirement: Managed watch pages show the staff photo
A managed recording's watch page SHALL show its owner's stored staff photo beside their name, inline in the page. It SHALL show initials when there is no photo.

#### Scenario: A stored photo
- **WHEN** a viewer unlocks a managed recording whose owner has a stored photo
- **THEN** the page shows the photo as a data URL beside the owner's name, and no other route serves the photo

#### Scenario: No photo
- **WHEN** the owner has no stored photo
- **THEN** the page shows the owner's initials, without a leading "Dr" or "Prof"

#### Scenario: Unmanaged share
- **WHEN** a viewer opens an unmanaged share
- **THEN** the page does not read or show a staff photo, and its initials keep any leading title
