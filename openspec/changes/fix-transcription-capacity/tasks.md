## 1. Transcription capacity

- [ ] 1.1 Make the transcription deadline 30 minutes plus four times the recording's length, with the added time capped at 4 hours. Apply the same per-row deadline in the stuck-job reset. Test both, the reset against PostgreSQL.
- [ ] 1.2 Run local Whisper at `nice -n 19` when `nice` is installed. Test the command.
- [ ] 1.3 Skip the transcription claim while every encoder slot is in use. Test it.

## 2. Verification

- [ ] 2.1 Run the Go checks.
- [ ] 2.2 Benchmark a 10-minute synthetic recording on a staging task the size of production, with the encode and the low-priority Whisper running together.
- [ ] 2.3 After the production release, read `transcribe: completed` against `transcribe: starting` in production logs for the first real recordings.
