## 1. Transcription capacity

- [x] 1.1 Make the transcription deadline 30 minutes plus four times the recording's length, with the added time capped at 4 hours. Apply the same per-row deadline in the stuck-job reset. Test both, the reset against PostgreSQL.
- [x] 1.2 Run local Whisper at `nice -n 19` when `nice` is installed. Test the command.
- [x] 1.3 Skip the transcription claim while every encoder slot is in use. Test it.
- [x] 1.4 At shutdown, requeue the running transcription with a new generation and stop claiming. Test it, the requeue against PostgreSQL.

## 2. Verification

- [x] 2.1 Run the Go checks.
- [x] 2.2 Benchmark a 10-minute synthetic recording on a staging task the size of production, with the encode and the low-priority Whisper running together. On a normal host the encode took 9.4 minutes and Whisper 20.0. On a slow host they took 16.0 and 36.4, both inside the new 70-minute deadline.
- [ ] 2.3 After the production release, read `transcribe: completed` against `transcribe: starting` in production logs for the first real recordings.
