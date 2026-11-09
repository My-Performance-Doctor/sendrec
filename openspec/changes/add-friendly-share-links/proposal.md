## Why

mpd-api sends patients SendRec watch links. On staging a link reads `https://d2oopdll9i8yvf.cloudfront.net/watch/Xk3_9aBcDeFgHiJkLmNoPq`: an AWS hostname patients don't know, then a path segment, then 22 characters of mixed case, dashes and underscores. It looks like spam or phishing. Josh asked on 2026-11-09 for links like `results.mpd.health/dg82ewfrwhb87c`.

## What Changes

- `/{shareToken}` serves the watch page, exactly as `/watch/{shareToken}` does. The long form keeps working for links already sent. Only the two token shapes match the short path, so it cannot hide an app route.
- New videos get a 16-character token of lowercase letters and digits. Existing tokens are unchanged.
- Every video watch link SendRec builds takes the short form: the share URL in the API and the app, MPD metadata `watchUrl`, notification, comment, digest and retention emails, oEmbed, and the page's canonical and Open Graph URLs. Playlist and embed links are unchanged.
- The CDK stack accepts a custom hostname and its `us-east-1` ACM certificate as two deploy contexts, both or neither. With both, CloudFront answers on that hostname, `BASE_URL` uses it, and the recordings bucket's CORS rule lists it beside the CloudFront hostname, so links already sent still play. Without them the stack keeps the CloudFront hostname, as today.
- Staging uses `results-staging.mpd.health`, after Staff OS's `app-staging.mpd.health`. Production uses `results.mpd.health` when it is deployed.

## Capabilities

### New Capabilities

- `friendly-share-links`: watch links use a short path, a lowercase token and, once configured, an MPD hostname.

### Modified Capabilities

None in the current main spec store. The staging deployment's DNS-free HTTPS still applies when the contexts are absent.

## Impact

`internal/video` link builders, token generator, MPD access guard and player script; the route in `internal/server`; three web pages that build share URLs; the CDK stack and its tests; deployment and MPD docs. No database or API shape changes.

Token entropy falls from 128 bits to about 83. Every watch route stays rate limited, and mpd-api publishes patient recordings only with a password.

The cutover needs work outside this repository, at the same time:

- An ACM certificate in `us-east-1` for the hostname. The developer SSO role is denied ACM in `us-east-1`, so an administrator requests it. `mpd.health` is hosted at GoDaddy, so its validation record and the hostname's CNAME to the SendRec distribution are added there.
- mpd-api's `SENDREC_ORIGIN` moves to the new origin in the same window. mpd-api refuses metadata whose `watchUrl` origin differs, so sends fail between the two deploys.
- The `mpd-staff-sendrec` Cognito client's callback, set by mpd-api's `sendrecCallbackUrl`, moves to `{new origin}/api/auth/mpd/callback`, or staff sign-in to SendRec's own pages fails.

Staff OS may keep calling SendRec on its CloudFront hostname: its origin is allow-listed, not compared with `BASE_URL`. Moving `STAGING_SENDREC_URL` is optional tidying.

## Approval and delivery

As with MPD-258, the change is applied with this proposal so it can be checked in a browser and with CDK synthesis. `CLAUDE.md` asks for Sami's review before apply, so the pull request stays unmerged until Sami has reviewed this proposal, and a human merges it. The hostname, certificate, DNS and mpd-api changes are separate operator steps after merge.
