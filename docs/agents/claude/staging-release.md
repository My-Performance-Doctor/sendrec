# Staging release from a Mac (no GitHub release roles)

The MPD workflows can't run: there are no repo variables or roles. This path took about 15 minutes on 2026-11-09.

1. `aws sso login --sso-session mpd` (the user approves in the browser). `mpd-staging` (DeveloperWorkloadAccess) can push to ECR, deploy CDK, run the migration task and update the service.
2. CI must pass on the merge commit. `govulncheck` can fail on a new advisory with no fix (GO-2026-6909, `crewjam/saml`); check whether the running image already has it before treating it as a release blocker.
3. Build and push. The image is tagged with the full revision; ECR is immutable.
   ```bash
   aws ecr get-login-password --region ap-southeast-2 | docker login --username AWS --password-stdin 537421187871.dkr.ecr.ap-southeast-2.amazonaws.com
   docker build --platform linux/amd64 --build-arg VERSION=${SHA:0:8} --label org.opencontainers.image.revision=$SHA -t 537421187871.dkr.ecr.ap-southeast-2.amazonaws.com/sendrec:$SHA .
   docker push …:$SHA   # then: aws ecr describe-images --image-ids imageTag=$SHA → digest
   ```
4. Toolchain: Gradle 8.14 needs JDK 21 (the Mac has 17 and 25). Untar Temurin 21 from `api.adoptium.net/v3/assets/latest/21/hotspot?architecture=aarch64&image_type=jdk&os=mac` into the scratchpad, after a `shasum` check, and set `JAVA_HOME`. The CDK CLI is cached at `~/.npm/_npx/*/node_modules/.bin/cdk` (2.1145.0); symlink it onto `PATH`, because `cdk-with-session.py` calls a bare `cdk`.
5. Contexts: read `sendrec-staging:<n>` with `describe-task-definition`. They are `activateMpdIntegration=true`, `bootstrap=false`, the nine `MPD_*` values from `environment`, and `mpdIntegrationSecretArn` from the `MPD_COGNITO_CLIENT_SECRET` `valueFrom` minus its `:KEY::` suffix. Keep them outside the repo, which is public. Sanity check: synthesizing with the *current* digest as `releaseImage` should equal the live template in every resource except `CDKMetadata`.
6. `synth … -o out`, then `diff -a out`. Expect only the two task definition images and the service's definition. Then `deploy -a out --require-approval never`. ECS swaps the single task, so expect a short outage.
7. `scripts/build_manifest.py`, then `scripts/promote_staging.py`. They run the repo's `promote()` and swap only the template-pair hash, because the production half needs the private grant sheet. Delete `infrastructure/__pycache__/` afterwards; importing the script creates it.
8. Verify: `/api/health` version, `/api/ready`, then the change itself. A T3 preview tab opened with `open:false` timed out on `preview_snapshot`; `preview_evaluate` with computed styles still works.

Production has no stack yet: gate 5 is Plane MPD-213. Never point this path at production.
