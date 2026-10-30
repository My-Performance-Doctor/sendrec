# Production decisions

Operator review of the production inputs for OpenSpec task 1.3 of `prepare-production-video-service`, held on 2026-10-12. Everything here applies to production only. Nothing is deployed. Production stays unactivated until a separate rollout approval, as the [production runbook](README.md) describes.

This repository is public. Account numbers, network and identity resource IDs, alert recipients and business volumes are kept in MPD's private records, not here.

## Summary

| # | Decision | Status |
|---|---|---|
| 1 | Run in the existing production VPC that mpd-api uses. Recordings bucket `mpd-video-recordings-production` | Approved |
| 2 | Alerts go to a dedicated SNS topic, `sendrec-production-alerts`, with email to MPD's operations automation mailbox | Approved. Topic creation deferred |
| 3 | Launch on the default CloudFront hostname | Approved |
| 4 | Promote the exact image digest that staging tested | Approved |
| 5 | mpd-api production changes for staff sign-in | Approved. Follows the empty stack deploy |
| 6 | Database class | db.t4g.small Multi-AZ approved on 26 October 2026 |
| 7 | S3 gateway endpoint, and NAT redundancy review | Approved |
| 8 | Recovery objectives | Fifteen-minute database RPO and four-hour RTO approved on 26 October 2026. Full recovery remains unmeasured |
| 9 | Two 2 vCPU, 4 GB Fargate tasks, about $278 a month at launch | Approved |

## 1. Account and network

SendRec production runs in the MPD production account in ap-southeast-2, in the same VPC as mpd-api production. A new VPC was rejected: the account boundary already separates staging from production, and a second VPC would need its own NAT and connectivity. `ProductionConfig` only requires that the VPC differ from staging's.

- Fargate tasks go in the private subnets across two availability zones.
- The internal ALB and RDS go in the isolated subnets.
- SendRec reaches mpd-api over its public HTTPS hostname, as staging does. No peering is needed.
- The recordings bucket is `mpd-video-recordings-production`. The name was free on 2026-10-12 but is not reserved.

## 2. Alerts

Alarms publish to a dedicated SNS topic named `sendrec-production-alerts`, with an email subscription for MPD's operations automation mailbox. The address is held in the private deployment record.

Creating the topic is deferred. On 2026-10-12, neither the production workload role nor the production Lambda development role was allowed `sns:CreateTopic`. Someone with production administration must create it before synthesis, because `ProductionConfig` requires an existing topic ARN. Having the SendRec stack create the topic and its subscription instead would need a code change, because the stack only accepts an existing topic today. The mailbox owner must confirm the AWS subscription email once.

Alarm gaps to close in task 2.4. The first three were added on 2026-10-26:

- Add OK actions, so the destination hears when an alarm clears. Design decision 3 requires recovery to be verified at the destination.
- Add an RDS free storage alarm, proposed below 4 GB free. The instance has 20 GB and no storage autoscaling.
- Add an RDS freeable memory alarm, proposed below 64 MB. Added at 64 MiB on micro and 128 MiB on small.
- Keep Target5xx at one error over two periods for the pilot, and review it after two weeks of data.

## 3. Hostname

Production launches on the managed `*.cloudfront.net` hostname, as staging does. Patient watch links carry this hostname. Adding a custom domain later is safe: the original CloudFront hostname keeps working after an alias is added, so links already sent stay valid. A custom domain needs an ACM certificate in us-east-1, a DNS record, and new callback and CORS values.

## 4. Image

Production runs the exact image digest that passed on staging, copied into a production ECR repository. `ProductionConfig` already requires a same-account digest. Staging got its immutable `sendrec` repository on 2026-10-26. Production still needs one, created in the deploy window.

## 5. Staff sign-in

mpd-api production needs a staff-pool hosted sign-in domain, a confidential `mpd-staff-sendrec` client and a production SendRec integration secret. mpd-api PR 219 adds these for staging only. Its production template keeps SendRec off.

The production client's callback contains SendRec's production CloudFront hostname, which exists only after the stack is created. The order is:

1. Deploy the empty SendRec production stack. It needs the alert topic from section 2 and an image digest in a production ECR repository from section 4 first; synthesis fails without either.
2. Open the mpd-api production change with the SendRec callback and hosted sign-in domain, then deploy mpd-api.
3. Put the new client ID and secret into `sendrec-production/mpd-integration`.
4. Activate the integration.

Staging used mpd-api's existing staff EHR client as a stopgap until PR 219 deployed, and moved to `mpd-staff-sendrec` on 2026-10-13. Production does not reuse the EHR client.

## 6. Database class

Sami approved db.t4g.small Multi-AZ on 26 October 2026, as recorded in the private grant-window sheet. The stack reads `productionDatabaseSize`, `micro` or `small`, and has defaulted to small since that date. The earlier cost estimate was about $37 a month for micro and $74 for small. These are planning estimates, not a fresh price check. Micro has 1 GiB of memory and small has 2 GiB. On staging, with one task and test traffic only, freeable memory fell to 74 MB. Production runs two tasks.

## 7. Network resilience

- Request an S3 gateway endpoint for the production VPC from `base-infrastructure`. It has no hourly charge. It removes NAT processing for media traffic, and it keeps task S3 access working if NAT is unavailable.
- Review NAT gateway redundancy with `base-infrastructure`. It affects every production workload in the VPC, not only SendRec.

## 8. Recovery objectives

Sami approved at most 15 minutes of database data loss and four hours to recover on 26 October 2026, as recorded in the private grant-window sheet. These are targets. The staging restore measured database restoration only, not a full recovery to working playback.

| Objective | Proposal | Meaning |
|---|---|---|
| Database data loss | At most 15 minutes | After a failure, the database is restored to a moment a few minutes before it. Recent metadata edits may need redoing |
| Media data loss | None for acknowledged uploads | Upload links refuse to replace an existing object, and versioning keeps the prior copy after any overwrite or deletion. Playback of a replaced or deleted object stays broken until an operator restores that version |
| Time to recover | 4 hours from alarm to patients watching again | The unmeasured estimate is about 2 hours: detect, decide, restore, cut over, verify |
| Database backups | 35 days with point-in-time recovery | A problem noticed weeks later can still be rolled back |
| Media retention | Versioning on, nothing expires or is deleted automatically | Holds until MPD approves a clinical retention policy |
| Staging | Seven-day backups, unchanged | |

Expiring old media versions in production waits on MPD's clinical retention policy, and the expiry must be at least the database point-in-time window, so a restored database never points at expired media. Until then, production's only lifecycle rule removes abandoned multipart uploads after seven days.

Follow-ups:

- The AWS restore drill in task 3.2 must measure both numbers, document the database cutover, and restore the media versions that restored rows reference.
- On 2026-10-26 a point-in-time restore of the staging database, 20 GB on db.t4g.micro, took 30 minutes to become available and 31 to verify, and RDS's restorable point trailed real time by about four minutes. Pointing the application at a restored database, the cutover itself, was not rehearsed.
- Backups currently live in the same account as the database. Cross-account copies or an AWS Backup vault lock would protect them from a compromised account. Proposed before volume grows past the pilot.
- Sydney residency rules out cross-region copies without a separate decision, so a full regional outage has no recovery target.

## 9. Cost

Approved: two always-on Fargate tasks of 2 vCPU and 4 GB across two availability zones. The size covers local Whisper transcription, which reached 100% CPU on staging. Two tasks keep the service up through a zone failure and through deploys.

Estimated monthly cost, at USD on-demand list prices before tax:

| Item | Monthly |
|---|---|
| Fargate, two tasks | $172.86 |
| RDS db.t4g.micro Multi-AZ, storage and backups | $43.75 |
| ALB | $21.40 |
| CloudWatch, Secrets Manager, CloudFront pages, ECR and cross-AZ traffic | $8.05 |
| **Fixed** | **about $246** |
| Storage, egress and NAT processing, assumed 300 ten-minute recordings a month | about $32 in month 1, about $63 by month 12 |
| **Total** | **about $278 in month 1, about $309 by month 12** |

Choosing db.t4g.small adds $37.23 a month. A one-year Compute Savings Plan would lower the Fargate line, but none is assumed. The volume line rests on an assumption: replace it with real Vimeo upload and playback figures when they are available.

## Approved inputs and remaining resources, updated 2026-10-30

The alert destination, db.t4g.small Multi-AZ and recovery targets were approved on 26 October. The production topic, confirmed subscription and Cognito client still need creation in their grant windows. The production client needs the CloudFront callback from the empty stack. Window A remains subject to Josh's go and Sami's production maintenance grant.

| Input | Recommended default | Why |
|---|---|---|
| SendRec's production Cognito client | A new confidential `mpd-staff-sendrec` client in the production staff pool, added by an mpd-api production change like PR 219: authorization code with PKCE, `openid email profile`, callback and sign-out on SendRec's production CloudFront host at `/api/auth/mpd/callback`, and a hosted sign-in domain on the staff pool. Its secret goes into `sendrec-production/mpd-integration` through the secret workflow. `MPD_ALLOWED_CLIENT_IDS` lists it and Staff OS's production client; `MPD_ALLOWED_ORIGINS` lists Staff OS's production origin only | The callback host exists only after the empty stack deploys, so the client follows it, as section 5 orders. Production does not reuse the EHR client |
| Alert email | One email subscription on `sendrec-production-alerts` for MPD's operations automation mailbox, chosen on 2026-10-12. The address stays in the private record. The mailbox owner confirms AWS's subscription email in the window | Every alarm now sends both the fault and the recovery, so the mailbox shows when to act and when it cleared |
| Database size | db.t4g.small Multi-AZ, $37.23 a month more than micro | Staging micro fell to 74 MB freeable memory with one task and test traffic; production runs two tasks. On micro, the free-memory alarm's 64 MiB floor would sit close to normal use; on small it fires below 128 MiB |
| Recovery objectives | Approved: at most 15 minutes of database data loss and four hours from alarm to patients watching again | The backup alarm fires when the restorable point is 15 minutes old. The staging restore took 31 minutes. Full application recovery still needs a drill |
