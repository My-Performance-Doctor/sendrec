## 1. Short links

- [x] 1.1 Serve `/{shareToken}` for 16-character lowercase and 22-character base64url tokens through the MPD public access guard, count it as a page there, and pass the token to the MPD player in `data-share-token`. Test the route, an app path, a protected MPD short link and the player script on the short path.
- [x] 1.2 Generate new share tokens as 16 lowercase letters and digits. Test the shape.
- [x] 1.3 Build every video watch link as `{BASE_URL}/{shareToken}`: API share URLs, MPD metadata, emails, oEmbed, canonical and Open Graph URLs, and the app's copied links. Update tests.

## 2. Custom hostname

- [x] 2.1 Accept `domainName` and `domainCertificateArn` contexts, both or neither, add the alias and certificate to the distribution, set `BASE_URL`, and keep the CloudFront origin in the recordings CORS rule. Test both cases and each invalid input.
- [ ] 2.2 Document the certificate, GoDaddy DNS, deploy contexts and the mpd-api and Cognito changes in the infrastructure README, and the short path in the MPD docs.

## 3. Staging

- [ ] 3.1 After merge: an administrator requests the `us-east-1` certificate for `results-staging.mpd.health`, and its validation record and a CNAME to the SendRec distribution are added at GoDaddy.
- [ ] 3.2 Deploy staging with the two contexts, together with mpd-api's `SENDREC_ORIGIN` and `sendrecCallbackUrl`. Send a synthetic recording and confirm the patient link is `https://results-staging.mpd.health/{16 characters}`, opens through its password gate in Chrome, Safari and Firefox, and that an earlier CloudFront `/watch/` link still plays.
