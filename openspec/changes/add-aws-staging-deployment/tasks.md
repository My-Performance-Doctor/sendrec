## 1. Reconcile the interrupted draft

- [x] 1.1 Obtain proposal approval, inspect the staged draft and bind this change; verify `openspec validate add-aws-staging-deployment --strict` passes before implementation.
- [x] 1.2 Add repository operating instructions and reconcile the draft decision log with this approved design; verify scope-check reports no unexplained files.

## 2. Make the container work on AWS

- [x] 2.1 Finish role-credential fallback and partial-credential validation; verify focused tests cover explicit local keys and temporary AWS credentials in upload and download signing.
- [x] 2.2 Make database URL assembly safe for reserved characters and validate required fields; verify synthetic entrypoint tests cover Compose passthrough, ECS fields and invalid input without printing credentials.
- [x] 2.3 Pin and verify the local model download and make initialization failures visible; verify missing, cached, checksum-failed and successful model paths with synthetic entrypoint fixtures.

## 3. Complete infrastructure and review

- [x] 3.1 Finish the dedicated database, bucket and service configuration; verify template assertions cover account, region, subnet placement, least-privilege role access, secrets injection and retained state.
- [x] 3.2 Replace HTTP application forwarding and public registration defaults with the HTTPS and restricted-bootstrap design, using AWS-managed CloudFront HTTPS with a private VPC origin; verify assertions cover disabled caching, restricted bootstrap and disabling registration before widening access.
- [x] 3.3 Add exact DNS-free deployment, owner-bootstrap and retained-resource instructions; verify the documented commands match the synthesized stack and never put secrets in command arguments.
- [x] 3.4 Run relevant Go and entrypoint tests, Java checks, Docker build and CDK synthesis; inspect the complete template and verify every acceptance control is present.
- [ ] 3.5 Commit completed task sections by explicit path, open a PR and run the required review loop; verify its review record and checks, link it to this thread and leave merging to a human.

## 4. Deploy and prove staging behavior

- [ ] 4.1 Deploy the reviewed code revision with AWS-managed CloudFront HTTPS and a private VPC origin under restricted access; verify the managed hostname, target health and completed migrations.
- [ ] 4.2 While bootstrap access is restricted, verify requests outside the allowed CIDR fail and owner registration inside it succeeds; then disable registration before opening the approved access, and verify owner login succeeds while unrestricted registration fails.
- [ ] 4.3 Run synthetic upload or recording, private-object denial, password-protected playback, local VTT generation and signed-webhook checks; verify every denied request has a successful authorized control and report any transcript limits.
- [ ] 4.4 Replace the service task and verify synthetic video persistence and processing recovery; save the exact revision, deployed outputs and check results in the deployment handoff, then validate and archive only after required work is complete.
