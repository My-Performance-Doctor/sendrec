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
| 6 | Database class | Deferred to the move to production |
| 7 | S3 gateway endpoint, and NAT redundancy review | Approved |
| 8 | Recovery objectives | Proposed |
| 9 | Two 2 vCPU, 4 GB Fargate tasks, about $278 a month at launch | Approved |

## 1. Account and network

SendRec production runs in the MPD production account in ap-southeast-2, in the same VPC as mpd-api production. A new VPC was rejected: the account boundary already separates staging from production, and a second VPC would need its own NAT and connectivity. `ProductionConfig` only requires that the VPC differ from staging's.

- Fargate tasks go in the private subnets across two availability zones.
- The internal ALB and RDS go in the isolated subnets.
- SendRec reaches mpd-api over its public HTTPS hostname, as staging does. No peering is needed.
- The recordings bucket is `mpd-video-recordings-production`. The name was free on 2026-10-12 but is not reserved.

## 2. Alerts

Alarms publish to a dedicated SNS topic named `sendrec-production-alerts`, with an email subscription for MPD's operations automation mailbox. The address is held in the private deployment record.

Creating the topic is deferred. On 2026-10-12, neither the production workload role nor the production Lambda development role was allowed `sns:CreateTopic`. Either the SendRec production stack creates the topic and its subscription, or someone with production administration creates it before synthesis, because `ProductionConfig` requires an existing topic ARN. The mailbox owner must confirm the AWS subscription email once.

Alarm gaps to close in task 2.4:

- Add OK actions, so the destination hears when an alarm clears. Design decision 3 requires recovery to be verified at the destination.
- Add an RDS free storage alarm, proposed below 4 GB free. The instance has 20 GB and no storage autoscaling.
- Add an RDS freeable memory alarm, proposed below 64 MB.
- Keep Target5xx at one error over two periods for the pilot, and review it after two weeks of data.

## 3. Hostname

Production launches on the managed `*.cloudfront.net` hostname, as staging does. Patient watch links carry this hostname. Adding a custom domain later is safe: the original CloudFront hostname keeps working after an alias is added, so links already sent stay valid. A custom domain needs an ACM certificate in us-east-1, a DNS record, and new callback and CORS values.

## 4. Image

Production runs the exact image digest that passed on staging, copied into a production ECR repository. `ProductionConfig` already requires a same-account digest. Neither account has a SendRec application ECR repository yet, so creating one is part of task 2.2.

## 5. Staff sign-in

mpd-api production needs a staff-pool hosted sign-in domain, a confidential `mpd-staff-sendrec` client and a production SendRec integration secret. mpd-api PR 219 adds these for staging only. Its production template keeps SendRec off.

The production client's callback contains SendRec's production CloudFront hostname, which exists only after the stack is created. The order is:

1. Deploy the empty SendRec production stack.
2. Open the mpd-api production change with the SendRec callback and hosted sign-in domain, then deploy mpd-api.
3. Put the new client ID and secret into `sendrec-production/mpd-integration`.
4. Activate the integration.

Staging currently uses mpd-api's existing staff EHR client as a stopgap until PR 219 deploys. Production does not reuse it.

## 6. Database class

Decided at the move to production. The CDK pins db.t4g.micro Multi-AZ, which has 1 GiB of memory and costs about $37 a month. The recommended alternative is db.t4g.small Multi-AZ, which has 2 GiB and costs about $74. On staging, with one task and test traffic only, freeable memory fell to 74 MB. Production runs two tasks.

## 7. Network resilience

- Request an S3 gateway endpoint for the production VPC from `base-infrastructure`. It has no hourly charge. It removes NAT processing for media traffic, and it keeps task S3 access working if NAT is unavailable.
- Review NAT gateway redundancy with `base-infrastructure`. It affects every production workload in the VPC, not only SendRec.

## 8. Recovery objectives

Proposed. In plain terms, these are the worst case MPD accepts.

| Objective | Proposal | Meaning |
|---|---|---|
| Database data loss | At most 15 minutes | After a failure, the database is restored to a moment a few minutes before it. Recent metadata edits may need redoing |
| Media data loss | None for acknowledged uploads | Uploads go straight to versioned S3 and cannot be overwritten |
| Time to recover | 4 hours from alarm to patients watching again | The unmeasured estimate is about 2 hours: detect, decide, restore, cut over, verify |
| Database backups | 35 days with point-in-time recovery | A problem noticed weeks later can still be rolled back |
| Media retention | Versioning on, nothing expires or is deleted automatically | Holds until MPD approves a clinical retention policy |
| Staging | Seven-day backups, unchanged | |

Follow-ups:

- The AWS restore drill in task 3.2 must measure both numbers, document the database cutover, and restore the media versions that restored rows reference.
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
