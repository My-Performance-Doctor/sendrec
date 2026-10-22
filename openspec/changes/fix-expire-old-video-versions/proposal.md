## Why

The 22 October 2026 security audit found the recordings bucket has no lifecycle rule. On a versioned bucket, deleted or replaced media lives on as noncurrent versions with no end date, and abandoned multipart uploads keep their parts indefinitely.

## What Changes

- Staging's recordings bucket gets a lifecycle rule that expires noncurrent versions 30 days after they become noncurrent and aborts incomplete multipart uploads after 7 days. Staging is not versioned today, so the noncurrent part takes effect only if versioning is turned on.
- Production's recordings bucket gets only the multipart-upload cleanup. Its versions keep the recorded operator decision "versioning on, nothing expires": noncurrent expiry waits on MPD's clinical retention policy and must be at least the 35-day RDS point-in-time window, so a restored database never points at expired media. `docs/production/operator-decisions.md` says so.
- Versioning itself is unchanged in both environments.

## Capabilities

### New Capabilities

- `recordings-bucket-lifecycle`: how long old media versions and abandoned uploads stay in each environment's recordings bucket.

### Modified Capabilities

None.

## Impact

`SendRecStack.java`, its CDK test and `docs/production/operator-decisions.md`. A staging deploy applies the staging rule. Production changes only with a reviewed production release.

## Approval and delivery

Sami approved the audit fix on 2026-10-22 and waived PR review. MPD HQ limited production to the multipart cleanup the same day, pending Sami's decision on noncurrent expiry there.
