## 1. Provider

- [x] 1.1 Add the `aws` provider: upload, job, polling, caption segments, language selection and cleanup. Test it against a fake Transcribe API and in-memory storage.
- [x] 1.2 Build it from `TRANSCRIPTION_PROVIDER=aws` with the recordings bucket and region; test the factory.
- [x] 1.3 Skip the claim while encoding only for local Whisper; test both providers.
- [x] 1.4 Retry failed status checks until the deadline and starts three times; fall back to local Whisper when Transcribe cannot finish. Test throttled polls, a lost start response, a start that keeps failing and the fallback.
- [x] 1.5 Cancel the transcription worker at shutdown after the hand-back and wait for its cleanup; set the job as started before the start call; wait for an abandoned job's late output and delete it; delete every object version. Test each.
- [x] 1.6 Map common two-letter languages to their usual Transcribe locale.

## 2. Infrastructure

- [x] 2.1 Expire `transcribe/` objects and their noncurrent versions after one day in both environments.
- [x] 2.2 Add the `transcriptionProvider` context: environment and least-privilege Transcribe permissions for `aws`. Test both values.
- [x] 2.3 Limit job output to `transcribe/` in the recordings bucket, deny service-made object calls outside it, and remove expired delete markers there. Test the statements and rules.
- [x] 2.4 Record the Sydney price per audio hour from the price list in DEPLOYMENT.md.

## 3. Verification

- [x] 3.1 Run the Go and infrastructure checks.
- [x] 3.2 Run the provider against staging AWS with synthetic speech and confirm the caption segments and that nothing remains in `transcribe/` or in the job list. 10.6 minutes came back as 149 segments in 25 seconds; only Transcribe's empty write-check file remained.
- [x] 3.3 Deploy to staging with `transcriptionProvider=aws` and transcribe a synthetic recording end to end. A 20-minute recording took 25.6 seconds from claim to published transcript.
- [ ] 3.4 After review and merge, release to production with `transcriptionProvider=aws` and read `transcribe: completed` against `transcribe: starting` for the first real recordings.
