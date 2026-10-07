# SendRec for MPD

This fork evaluates SendRec as MPD's Vimeo replacement. `CLAUDE.md` is the canonical repository instruction file; `AGENTS.md` points here.

## Scope and delivery

- Use OpenSpec. Read the active change and validate it strictly before implementation. Proposals need Sami's review before apply. Humans merge PRs.
- Keep upstream changes small. MPD-specific patient mapping and integration belong in `mpd-api`, outside this staging change.
- Stage explicit paths and commit completed task sections separately. Read the global session rulebook for branch, review and reporting requirements.
- Use synthetic videos and identities during staging verification. Do not test live sending integrations or store patient identifiers, media or credentials in the repository.

## Local checks

```bash
python3 -B -m unittest discover -s infrastructure/tests -v
cd infrastructure
node tests/test_bootstrap_access.cjs
./gradlew test
```

Run `go test ./...` using the toolchain from `go.mod`, after building `web/dist` with `pnpm install --frozen-lockfile` and `pnpm build` in `web/`. The ffmpeg integration test also needs ffmpeg. CI runs database-dependent tests against a synthetic PostgreSQL service.

## AWS staging

Java CDK lives in `infrastructure/`. Read its README before deployment. Resources belong to account `537421187871`, region `ap-southeast-2`. The AWS-managed CloudFront hostname provides HTTPS without external DNS work. Its load balancer, database and service are private. Recordings use private S3 storage and temporary task-role credentials. Initial registration requires explicit bootstrap mode and a single allowed IPv4 address; normal deployment disables registration.

Secrets come from Secrets Manager through ECS. Never put secret values in arguments, logs or committed files. `infrastructure/DEPLOYMENT.md` records verified deployment evidence and remaining limits; a written runbook does not mean deployment happened.
