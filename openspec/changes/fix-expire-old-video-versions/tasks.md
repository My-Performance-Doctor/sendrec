## 1. Lifecycle rules

- [x] 1.1 Add the staging rule (noncurrent versions after 30 days, incomplete multipart uploads after 7) and the production rule (multipart uploads only), leave versioning unchanged, and assert both in the CDK test.
- [x] 1.2 Record in `docs/production/operator-decisions.md` that production noncurrent expiry waits on the clinical retention policy and must be at least the RDS point-in-time window.
