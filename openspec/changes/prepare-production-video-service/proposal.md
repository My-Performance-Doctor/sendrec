## Why

Staging proved the core media path but uses a single task and has no completed production recovery or browser matrix. MPD needs reproducible deployment and measured operational checks before a controlled production pilot.

## What Changes

- Add environment-specific production infrastructure and staged release/rollback controls using the existing Java CDK stack patterns.
- Add alerts, backup restoration, durable-event recovery and deployment evidence for the existing media service.
- Verify standalone recording and protected playback across Chrome, Safari and Firefox, and verify the future-client API contract without implementing Staff OS.
- Keep production activation and irreversible retention cleanup as explicit operator decisions after evidence is available.

## Capabilities

### New Capabilities

- `production-video-service`: Add environment-specific production infrastructure and staged release/rollback controls using the existing Java CDK stack patterns.

### Modified Capabilities

None in the current main spec store. Integration behavior is defined by the new capability; existing provider-specific requirements remain in force.

## Impact

Java CDK, deployment automation, operational runbooks and synthetic evaluation. Depends on the other four proposals and closing the existing staging change with honest verification evidence. Keeps Sydney hosting, private encrypted media and database storage. Does not authorize production deployment, new clinical transcription claims, retained-resource deletion or legacy-video migration.

## Approval and delivery

This proposal is one of five changes requested on 2026-10-08. Proposal review and explicit apply authorization precede implementation. Each implementation change lands through a human-merged PR. All examples and tests use synthetic identities and media.
