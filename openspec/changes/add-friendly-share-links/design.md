## Context

`BASE_URL` comes from the CloudFront distribution's own hostname, and every watch link is `{BASE_URL}/watch/{token}`. Tokens are 16 random bytes in unpadded base64url. The SPA is served by the router's not-found handler, so a bare `/{param}` route would capture `/login` and every other app path.

## Decisions

### The short path matches only token shapes

The route is `/{shareToken:(?:[a-z0-9]{16}|[A-Za-z0-9_-]{22})}`. chi prefers static routes, and no app path is 16 lowercase alphanumerics or 22 base64url characters, so `/login`, `/library`, `/forgot-password` and asset files still reach the SPA. The old shape is accepted so every existing video gets the short link too.

The handler is the existing `WatchPage`, behind the same `MPDPublicAccess` guard. The guard renders the password form for page paths and enforces access for API paths; it now counts `/{token}` as a page. The MPD player script read the token from a three-part `/watch/{token}` path, so the watch and embed pages now pass it in `data-share-token`.

A redirect from `/{token}` to `/watch/{token}` was rejected: it costs a round trip and the address bar ends on the long form.

### Tokens are 16 lowercase alphanumerics

36 characters, drawn from `crypto/rand` with rejection sampling so each is uniform: log2(36^16) ≈ 82.7 bits. That keeps guessing infeasible behind the watch rate limits, and patient links carry a password as well. Lowercase reads cleanly and survives a reader retyping it. Existing 22-character tokens are not rewritten, because they are in links already sent.

### One host, set by two contexts

`-c domainName=results-staging.mpd.health -c domainCertificateArn=arn:aws:acm:us-east-1:…` adds the alias and certificate to the distribution and makes `https://{domainName}` the `BASE_URL`. Moving the whole service, not only share links, keeps one origin for cookies, the MPD sign-in callback, CSP and CORS, and needs no new setting in the server. The CloudFront hostname keeps answering, so `/watch/{token}` links already sent still open, and the recordings bucket's CORS rule keeps it so their players still load captions.

The contexts follow the existing deployment convention: pass them on every deploy and record them in `DEPLOYMENT.md`. Leaving them out reverts `BASE_URL`, which mpd-api detects at once as a metadata origin mismatch. The certificate ARN must be in `us-east-1` and in the stack's account.

A separate share-only hostname was rejected. It needs a second base URL in the server and a second accepted origin in mpd-api, for no gain to patients.

## Risks

- **Cutover order.** mpd-api must move `SENDREC_ORIGIN` in the same window as SendRec's `BASE_URL`. Until both are deployed, sends fail closed with `provider_metadata_mismatch`; nothing is sent to the wrong host.
- **Staff on the old hostname.** SendRec's own staff sign-in accepts only `BASE_URL` as its origin, so staff use the new hostname for SendRec's pages. Staff OS is unaffected.
- **DNS outside AWS.** `mpd.health` is at GoDaddy. Until the CNAME exists, the distribution answers only on its CloudFront hostname, so set the contexts only after DNS resolves.
