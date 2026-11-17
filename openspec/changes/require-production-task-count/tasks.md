## 1. Task count

- [x] 1.1 Require `productionTaskCount` (0 to 4) for production synthesis and set it on the service.
- [x] 1.2 Emit no task count for staging, using CDK's `removeDefaultDesiredCount` flag set on the stack.
- [x] 1.3 Test a missing or invalid count, counts 0, 2 and 4, and staging's absent count.

## 2. Stable alarms

- [x] 2.1 Build each alarm's metric map in a fixed order, and test it.

## 3. Release documentation

- [x] 3.1 List migrations 000079, 000080 and 000081 in order in `infrastructure/DEPLOYMENT.md`, with `productionTaskCount`.
- [x] 3.2 Add `productionTaskCount` to the production synthesis inputs in `docs/production/README.md`.
- [ ] 3.3 Prove on staging that a deploy at 2 tasks keeps 2 tasks.
