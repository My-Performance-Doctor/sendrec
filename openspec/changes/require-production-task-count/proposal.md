## Why

On 16 November a staging rehearsal showed that a plain stack deploy resets a running service to the template's task count. Production's template said 0 while production ran 2 tasks, so the next ordinary deploy would have stopped both production tasks until a promote raised the count again. The 17 November release had to deploy an assembly patched by hand to hold the count (MPD-502).

## What Changes

- Production synthesis requires `productionTaskCount`, a whole number from 0 to 4. The production service template states that count, so a deploy keeps production at the count it names. A missing or invalid value fails synthesis.
- Staging's service template names no task count. CloudFormation then leaves a running staging service's count alone on every deploy; a new service starts with 1.
- Alarm math metrics are emitted in a fixed order, so an unchanged alarm no longer shows as modified on every deploy.
- `infrastructure/DEPLOYMENT.md` lists the migrations the next release must run first, in order: 000079, 000080 and 000081.

## Impact

`infrastructure/src/main/java/com/mpd/sendrec/infrastructure/SendRecStack.java`, its test, `infrastructure/DEPLOYMENT.md` and `docs/production/README.md`. The next production deploy passes `productionTaskCount=2`. No application code or migration.

## Approval and delivery

Sami's 17 November ruling has MPD's side fix, merge and deploy SendRec changes. HQ assigned MPD-502, due 20 November.
