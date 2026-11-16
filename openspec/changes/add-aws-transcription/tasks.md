## 1. Provider

- [ ] 1.1 Add the `aws` provider: upload, job, polling, caption segments, language selection and cleanup. Test it against a fake Transcribe API and in-memory storage.
- [ ] 1.2 Build it from `TRANSCRIPTION_PROVIDER=aws` with the recordings bucket and region; test the factory.
- [ ] 1.3 Skip the claim while encoding only for local Whisper; test both providers.

## 2. Infrastructure

- [ ] 2.1 Expire `transcribe/` objects and their noncurrent versions after one day in both environments.
- [ ] 2.2 Add the `transcriptionProvider` context: environment and least-privilege Transcribe permissions for `aws`. Test both values.

## 3. Verification

- [ ] 3.1 Run the Go and infrastructure checks.
- [ ] 3.2 Run the provider against staging AWS with synthetic speech and confirm the caption segments and that nothing remains in `transcribe/` or in the job list.
- [ ] 3.3 Deploy to staging with `transcriptionProvider=aws` and transcribe a synthetic recording end to end.
