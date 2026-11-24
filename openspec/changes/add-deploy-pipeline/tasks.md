## 1. Migrations inside each deploy

- [x] 1.1 Add `migration-runner.py`: run the migration definition once, wait for exit 0, stop and fail before the Lambda limit, do nothing on delete or while the stack rolls back.
- [x] 1.2 Add the `MigrationRun` custom resource to controlled releases, after the database and before the service, with only the grants it needs.
- [x] 1.3 Test the runner (Python) and the resource's wiring and grants (CDK).

## 2. Pipeline

- [x] 2.1 Add `PipelineConfig`: read the settings and both stage contexts, refuse pipeline-owned keys and bootstrap mode, build both image URIs from the digest.
- [x] 2.2 Add `PipelineStack`: source, Build (image then synth, named role, no lookup role), staging stage with Verify, `ApproveProduction`, production stage with PromoteImage and Verify.
- [x] 2.3 Add `infrastructure/pipeline/build-image.sh`, `promote-image.sh` and `verify-version.sh`, with tests.
- [x] 2.4 Add the `pipeline` target to `SendRecApp` and write `sendrec-release.json` into the assembly.
- [x] 2.5 Test the stage order, the configuration checks, the image URIs, the steps' roles and grants, and that no per-commit value enters the pipeline definition.

## 3. Nightly release

- [x] 3.1 Port the staff app's `nightly_release.py` to `NightlyRelease.java` with SendRec's names and 17:20 Sydney.
- [x] 3.2 Test it by running the synthesized definition against a fake pipeline: approve, hold, supersede, transitions, deploying, changed pipeline, one release per firing, read cap; and the schedule, triggers, topics and grants.
- [x] 3.3 Check the synthesized definition against the staff app's, with names substituted.

## 4. Review fixes

- [x] 4.1 Name each stage's network in its context and drop the CDK lookup roles from the Build role.
- [x] 4.2 Wait for GitHub CI before staging; run the Python and Node infrastructure tests in Build.
- [x] 4.3 Refuse a reused image without the commit's revision label; stop on any production tag-read error but "not found".
- [x] 4.4 Make the migration run wait for its task roles, answer within 16 minutes and never retry the Lambda.

## 5. Workflows and documentation

- [x] 5.1 Remove `mpd-build-candidate.yml` and `mpd-promote.yml`.
- [x] 5.2 Document the pipeline, the nightly release, the hold and the one-time deploy steps in `infrastructure/README.md`; mark the hand runbook as the fallback; note the change in `DEPLOYMENT.md`.

## 6. Review desk fixes

- [x] 6.1 Exit 0 from a migration run whose clean database is newer than the image; test a forward release and a rollback on a synthetic database.
- [x] 6.2 Leave an overrunning migration to finish instead of stopping it; document recovering a dirty schema and what a rollback leaves in the schema.
- [x] 6.3 Port cortex-ai #132: ignore a finished run still shown waiting at an idle gate, narrow the approval snapshot, read the token only from a waiting approval, read again after a changed confirmation.

## 7. CI result without a GitHub token

- [x] 7.1 Add `ci.yml`'s `record-result` job: push to `main` only, after every check, OIDC through a pinned action, role ARN from a secret, success or failure recorded for the commit.
- [x] 7.2 Add the results table, the OIDC provider (imported by ARN when given) and the record role, trusted only by `main` of this repository by immutable id, allowed only `PutItem`.
- [x] 7.3 Make `WaitForGitHubCI` read the record with `GetItem` and no secret; remove the token.
- [x] 7.4 Test the trust, the single permission, the absent secret, the job's conditions, and success, failure, an older commit and the timeout.
- [x] 7.5 Rewrite D11 and the README's one-time steps.
