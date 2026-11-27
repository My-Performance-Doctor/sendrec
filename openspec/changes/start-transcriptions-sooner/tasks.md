## 1. Start transcriptions sooner

- [x] 1.1 Wake the transcription worker when a job is enqueued and when a running job finishes. Keep the 5-second poll. Test that an enqueued job starts without waiting for the poll.
- [x] 1.2 Run up to `MAX_CONCURRENT_TRANSCRIPTIONS` jobs at once (default 3), one at a time for local Whisper. Track every running job so shutdown requeues and waits for all of them. Test the limit and that the next job starts when one finishes.
- [x] 1.3 Run local Whisper one job at a time per process, and claim no further job while it is busy. Test both.
- [x] 1.4 Check an Amazon Transcribe job every second for its first 30 seconds. Test it.

## 2. Verification

- [x] 2.1 Run the Go checks and lint.
- [ ] 2.2 After the release, compare `transcribe-worker: claimed video` against `job: enqueued`, and `transcribe: completed` against `transcribe: starting`, for new recordings.
