## 1. Short links

- [x] 1.1 Serve `/{shareToken}` for 16-character lowercase and 22-character base64url tokens through the MPD public access guard, count it as a page there, and pass the token to the MPD player in `data-share-token`. Test the route, an app path, a protected MPD short link and the player script on the short path.
- [x] 1.2 Generate new share tokens as 16 lowercase letters and digits. Test the shape.
- [x] 1.3 Build every video watch link as `{BASE_URL}/{shareToken}`: API share URLs, MPD metadata, emails, oEmbed, canonical and Open Graph URLs, and the app's copied links. Update tests.

## 2. Custom hostname

- [x] 2.1 Let an environment's committed config name a hostname and its `us-east-1` certificate; add the alias and certificate to the distribution, set `BASE_URL`, and keep the CloudFront origin in the recordings CORS rule. Test both cases, that a context cannot set it, and each invalid input.
- [x] 2.2 Document the certificate, GoDaddy DNS, deploy contexts and the mpd-api and Cognito changes in the infrastructure README, and the short path in the MPD docs.

## 3. Staging

- [ ] 3.1 After merge: an administrator requests the `us-east-1` certificate for `results-staging.mpd.health`, and its validation record and a CNAME to the SendRec distribution are added at GoDaddy.
- [ ] 3.2 Commit the hostname and certificate in `StagingConfig` through a reviewed pull request, then deploy staging together with mpd-api's `SENDREC_ORIGIN` and `sendrecCallbackUrl`. Send a synthetic recording and confirm the patient link is `https://results-staging.mpd.health/{16 characters}`, opens through its password gate in Chrome, Safari and Firefox, and that an earlier CloudFront `/watch/` link still plays.

## 4. Production

- [x] 4.1 An administrator requested the `us-east-1` certificate for `results.mpd.health` in the production account, and it was issued on 13 November 2026.
- [x] 4.2 Commit `results.mpd.health` in `ProductionConfig`, taking its certificate from the required input `productionDomainCertificateArn`. Test the alias, `BASE_URL`, a missing input and a certificate from another account or region.
- [ ] 4.3 Add a CNAME at GoDaddy from `results.mpd.health` to the production distribution's `*.cloudfront.net` name, and merge 4.2 only once it resolves.
- [ ] 4.4 In one window, deploy production SendRec with `productionDomainCertificateArn`, set the `origin` of mpd-api's production integration secret to `https://results.mpd.health`, deploy mpd-api with the matching `sendrecCallbackUrl`, and deploy Staff OS with its production SendRec URL on the new hostname. Send a synthetic recording and confirm its link is `https://results.mpd.health/{16 characters}`, the Staff OS preview plays, and an earlier CloudFront link still opens.
