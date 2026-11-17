## Why

On 16 November a staging rehearsal showed that a plain stack deploy resets a running service to the template's task count. Production's template said 0 while production ran 2 tasks, so the next ordinary deploy would have stopped both production tasks until a promote raised the count again. The 17 November release had to deploy an assembly patched by hand to hold the count (MPD-502).

## What Changes

- Production synthesis requires `productionTaskCount`, a whole number from 0 to 4. The production service template states that count, so a deploy keeps production at the count it names. A missing or invalid value fails synthesis.
- Staging's service template names no task count: a null `desiredCount` leaves `DesiredCount` out. CloudFormation then leaves a running staging service's count alone on every stack deploy; a new service starts with 1. A staging promote through `build_manifest.py` and `promote-release.py` still sets 1 task.
- `promote-release.py` accepts a production count of 1 to 4, and only when it equals the reviewed production template's `DesiredCount`, so a release cannot scale to a count the next stack deploy would undo.
- Alarm math metrics are emitted in a fixed order, so an unchanged alarm no longer shows as modified on every deploy.
- `infrastructure/DEPLOYMENT.md` lists the migrations the next release must run first, in order: 000079, 000080 and 000081.

## Impact

`SendRecStack.java`, `ProductionConfig.java`, `SendRecApp.java` and their test, `infrastructure/promote-release.py` and its test, `infrastructure/DEPLOYMENT.md`, `infrastructure/README.md`, `docs/production/README.md` and the release runbooks in `docs/agents/claude/`. The next production deploy passes `productionTaskCount=2`. The production stack's description changes. No application code or migration.

## Approval and delivery

Sami's 17 November ruling has MPD's side fix, merge and deploy SendRec changes. HQ assigned MPD-502, due 20 November.
