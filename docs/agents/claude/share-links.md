# Share links, hostnames and DB tests

- Every video watch link goes through `WatchLink(baseURL, token)` in `internal/video/video_query.go`, a wrapper over `sharelink.Watch`: `{BASE_URL}/{token}` for tokens matching `ShareTokenPattern`, `/watch/{token}` for any other shape. The web app's copy is `watchPath` in `web/src/utils/shareLink.ts`. `internal/integration/handler.go` calls `sharelink.Watch` directly, because `video` imports `integration` through `mpd`. Grep `"/watch/"` in non-test Go to find stragglers; playlist (`/watch/playlist/`) and embed links stay long on purpose.
- The short route is `/{shareToken:` + `video.ShareTokenPattern` + `}` in `internal/server/server.go`. Wrap a chi regex alternation in `(?:…)`: chi adds `^…$` around the pattern, and a bare `|` breaks the anchoring. The SPA is the router's NotFound, so a looser pattern would swallow `/login`.
- `MPDPublicAccess` (`internal/video/mpd_access.go`) renders the password form only for page paths; API paths enforce access. Any new page path must be added to its `page` check. The MPD player script needs `data-share-token` on `#player`; its path fallback only parses `/watch/{token}`.
- mpd-api (`application/src/video_report/sendrec/adapter.py`) rejects metadata whose `watchUrl`/`embedUrl` origin differs from `SENDREC_ORIGIN`. Any `BASE_URL` change must ship with that setting and the Cognito `sendrecCallbackUrl`.
- The custom hostname is `EnvironmentConfig.customDomain()` (committed config, not a context) so no deploy can drop the alias by omission. Test a config override with an anonymous `EnvironmentConfig` delegating to `StagingConfig.INSTANCE` (see `stackWithDomain` in `SendRecStackTest`).
- Vite's proxy matches regex keys (those starting `^`) against the URL with its query string, so anchor token paths with `(?:\\?|$)`, not `$`.
- `mpd.health` DNS is at GoDaddy (`domaincontrol.com`), not Route 53. The developer SSO role is denied ACM and CloudFormation in `us-east-1`; CloudFront certs need an administrator. Staging distribution: `E1SOB3QVORZR22` / `d2oopdll9i8yvf.cloudfront.net`.
- DB-backed Go tests skip without `TEST_DATABASE_URL`. Fast path:
  `docker run -d --name pg-sendrec-x -e POSTGRES_USER=sendrec -e POSTGRES_PASSWORD=testpass -e POSTGRES_DB=sendrec -p 127.0.0.1:55418:5432 postgres:18-alpine`
  then `TEST_DATABASE_URL='postgres://sendrec:testpass@127.0.0.1:55418/sendrec?sslmode=disable' go test ./...` (video package ~100 s). Remove the container after.
- `cdk` isn't installed globally; `cdk-with-session.py` calls `cdk` on PATH. Shim: a script `exec npx -y aws-cdk@2 "$@"` named `cdk` in a scratch `bin` on PATH. Only JDK 25 is installed locally; Gradle tests pass on it.
- OpenSpec CLI isn't installed: `npx -y @fission-ai/openspec@latest validate <change> --strict`.
