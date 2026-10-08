## 1. Release prerequisites

- [ ] 1.1 Reconcile and close the existing staging change using actual merged-source review and verification evidence; validate and archive it without inventing the missing final review.
- [ ] 1.2 Confirm all four integration changes are human-merged and deployed to staging with matching contracts; verify their revisions, disabled production settings and synthetic evidence.
- [ ] 1.3 Capture the production account/VPC, identity references, operational alert destination, cost estimate and proposed recovery objectives for operator review; verify no live deployment is implied.

## 2. Infrastructure and release controls

- [ ] 2.1 Extend Java CDK with isolated production configuration, private encrypted resources and exact origins; verify synthesis and automated infrastructure assertions.
- [ ] 2.2 Add immutable-image promotion, a single migration task, staging checks and a manual production approval; verify failed readiness triggers a safe rollback rehearsal.
- [ ] 2.3 Configure proposed multi-task and Multi-AZ operation; run concurrent worker and interruption tests before confirming it is safe to scale.
- [ ] 2.4 Add health/readiness and backlog/processing/backup alarms with redacted logs; inject synthetic faults and verify the configured operator actually receives alerts.

## 3. Recovery and client verification

- [ ] 3.1 Configure reviewed production backup/versioning retention without automatic source-media deletion; verify managed user/workspace retention changes are refused and stale nonzero retention_days plus old warnings cannot cause worker deletion or later S3 purge. Keep staging and retained legacy resources untouched.
- [ ] 3.2 Restore a synthetic database and media set into isolation; measure RPO/RTO and verify decoding, passwords, captions, identities and pending-event recovery against the proposed objectives.
- [ ] 3.3 Complete the Chrome/Safari/Firefox recording/upload/watch/embed matrix with actual browser/OS versions, denied-device and interrupted-network cases; leave unavailable checks explicitly incomplete.
- [ ] 3.4 Exercise the synthetic future-client contract, offboarding, service-token rotation and safe deployment rollback; verify no Staff OS UI or live sending integration is required.

## 4. Production handoff

- [ ] 4.1 Publish the measured evidence, immutable revisions, configuration diff and operating runbook; verify secret scans and strict OpenSpec validation pass.
- [ ] 4.2 Open the readiness implementation PR and complete review/CI; present the concrete production rollout for explicit operator approval before deployment.
- [ ] 4.3 After separate rollout approval, deploy the empty production service and verify approved pilot checks; record the exact live state and preserve Vimeo service and retained resources.
