## 1. Task count

- [x] 1.1 Require `productionTaskCount` (1 to 4) for production synthesis and set it on the service.
- [x] 1.2 Emit no task count for staging: a null `desiredCount` leaves `DesiredCount` out of the template.
- [x] 1.4 Make `promote-release.py` accept a production count of 1 to 4 only when it equals the reviewed template's `DesiredCount`; staging still runs 1.
- [x] 1.3 Test a missing or invalid count (including 0), counts 1, 2 and 4, and staging's absent count.

## 2. Stable alarms

- [x] 2.1 Build each alarm's metric map in a fixed order, and test it.

## 3. Release documentation

- [x] 3.1 List migrations 000079, 000080 and 000081 in order in `infrastructure/DEPLOYMENT.md`, with `productionTaskCount`.
- [x] 3.2 Add `productionTaskCount` to the production synthesis inputs in `docs/production/README.md`.
- [x] 3.3 Prove on staging that a deploy at 2 tasks keeps 2 tasks.
