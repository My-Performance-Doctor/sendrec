## 1. Provider

- [x] 1.1 Add the `aws` provider: upload, job, polling, caption segments, language selection and cleanup. Test it against a fake Transcribe API and in-memory storage.
- [x] 1.2 Build it from `TRANSCRIPTION_PROVIDER=aws` with the recordings bucket and region; test the factory.
- [x] 1.3 Skip the claim while encoding only for local Whisper; test both providers.

## 2. Infrastructure

- [x] 2.1 Expire `transcribe/` objects and their noncurrent versions after one day in both environments.
- [x] 2.2 Add the `transcriptionProvider` context: environment and least-privilege Transcribe permissions for `aws`. Test both values.

## 3. Verification

- [x] 3.1 Run the Go and infrastructure checks.
- [x] 3.2 Run the provider against staging AWS with synthetic speech and confirm the caption segments and that nothing remains in `transcribe/` or in the job list. 10.6 minutes came back as 149 segments in 25 seconds; only Transcribe's empty write-check file remained.
- [x] 3.3 Deploy to staging with `transcriptionProvider=aws` and transcribe a synthetic recording end to end. A 20-minute recording took 25.6 seconds from claim to published transcript.
- [ ] 3.4 After review and merge, release to production with `transcriptionProvider=aws` and read `transcribe: completed` against `transcribe: starting` for the first real recordings.
