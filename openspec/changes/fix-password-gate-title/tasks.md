## 1. Generic gate title

- [x] 1.1 Render the watch and embed password and email-gate pages with a generic title; test that the recording title is absent.

## 2. Per-video lockout

- [x] 2.1 Add `share_password_attempts` and lock `VerifyWatchPassword` for 15 minutes after 10 wrong passwords in a 15-minute window, refusing when the state cannot be read; test the lockout, the correct password while locked, and the window reset against PostgreSQL.
