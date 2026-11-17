# Production and staging releases without the workflows (Nov 2026)

The `mpd-build-candidate` and `mpd-promote` workflows have never run, because no roles or variables are configured. Production (`SendRecProduction`, deployed by hand on 16 Nov) and staging are released from the live template.

## Image-only or small change, either environment

1. Staging image: build with `docker build --platform linux/amd64 --build-arg VERSION=${SHA:0:8} --label org.opencontainers.image.revision=$SHA -t 537421187871.dkr.ecr.ap-southeast-2.amazonaws.com/sendrec:$SHA .`, after `docker login` to staging ECR. It takes about 10 minutes under emulation. Push it, then read the digest from `ecr describe-images`.
2. Production image: copy the staging digest with `crane copy` (`GOBIN=<scratch>/bin go install github.com/google/go-containerregistry/cmd/crane@v0.20.2`; log in through `aws ecr get-login-password | crane auth login <registry> -u AWS --password-stdin` with a scratch `DOCKER_CONFIG`). Tag it with the full SHA. `crane digest` must match. `docker pull`/`push` can change the digest.
3. `aws cloudformation get-template --stack-name <stack> --template-stage Original`. Replace only what changes. For an image, swap the digest string; it appears twice, in the app and migration task definitions. For a code change with infrastructure, run a CDK synth with the live contexts and copy in only the changed resources (`scratchpad/merge_release.py` pattern: live template plus the named logical IDs from the synth).
4. **Task count:** synthesize production with `productionTaskCount` set to the count the service should run (normally 2); synthesis fails without it, and the template then carries that count, so no hand-patched `DesiredCount` is needed. Staging's template names no count, and a stack deploy keeps the count it runs. The first deploy after `require-production-task-count` shows the service's `DesiredCount` leaving staging's template; that is expected.
5. Check in-flight work first (`transcribe|normalize|transcode` in the last hours of logs). `create-change-set --capabilities CAPABILITY_IAM CAPABILITY_NAMED_IAM --parameters ParameterKey=BootstrapVersion,UsePreviousValue=true`, then `describe-change-set --include-property-values`. Large values show as `(Truncated-Signature)` hashes, so trust the local template diff. A bucket policy shows `Dynamic` from the bucket's `Arn`; that is not a real change.
6. `execute-change-set`, `wait stack-update-complete`, `ecs wait services-stable`, then `/api/health` for the version and `/api/ready` three times.
7. A release with migrations runs them first, because the app runs with `MIGRATIONS_MODE=skip`: deploy a change set that changes only the migration task definition to the release image, run that definition once with `aws ecs run-task` and confirm exit 0, then deploy the application change, then run the guarded promote (`promote-release.py`), which reruns the now no-op migration and checks readiness.

`mpd-prod` (ProductionWorkloadAccess) cannot read CloudFormation or CodePipeline; use `mpd-prod-admin`. Never put production account IDs or hostnames in this public repo.

## Staging database without a login

Register a temporary `postgres:18-alpine` Fargate task with the migration execution role and the migration definition's `DB_*` secrets. Pass SQL through an env var to `psql "host=$DB_HOST … sslmode=require"`, read the `e2e-psql/psql/<task>` log stream, then deregister the task. Re-transcribe: `UPDATE videos SET transcript_status='pending', transcript_generation=transcript_generation+1, transcript_started_at=NULL, updated_at=now() WHERE id=…`. Staging only, with synthetic rows only.
