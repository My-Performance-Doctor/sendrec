# Runtime base, CA check and staging deploys

- `gh` in this clone defaults to upstream `sendrec/sendrec` because of the `upstream` remote. Run `gh repo set-default My-Performance-Doctor/sendrec` first, or pass `-R`.
- Base image: `Dockerfile.base` → ECR `sendrec-base` (staging account), pinned by digest in `Dockerfile` `ARG BASE_IMAGE`. Rebuild with `--pull --no-cache --platform linux/amd64` from the repo root.
- The CA check is `scripts/check-ca-roots.sh`, run through `RUN --mount=type=bind` in all three Dockerfiles. Test it fast without building: `docker run --rm -v $PWD/scripts/check-ca-roots.sh:/c.sh:ro alpine:3.21.8 sh /c.sh` (3.21.8 should fail on three eMudhra roots; `alpine:3.21` plus `apk add ca-certificates` should pass with 118).
- Find roots a ca-certificates update removed: diff `/etc/ssl/certs/ca-certificates.crt` between `alpine:3.21.N` tags by SHA-256 fingerprint.
- CDK asset builds pull the base as `cdk-hnb659fds-image-publishing-role-…`; `infrastructure/sendrec-base-repository-policy.json` grants it. To check: assume that role, `docker login` with a temporary `DOCKER_CONFIG`, then pull the digest.
- Read deployed non-secret env: `AWS_PROFILE=mpd-staging aws ecs describe-task-definition --task-definition sendrec-staging:<n>`.
- Local stacks: `make docker-up` builds the base, then pins Compose to `BUILDX_BUILDER=$(docker context show)`, because Compose on a docker-container builder can't see local images.
- Merges to main deploy nothing. The MPD build and promote workflows only run on `workflow_dispatch`.
