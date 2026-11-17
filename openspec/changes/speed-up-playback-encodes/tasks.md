## 1. One conversion per video

- [x] 1.1 Add `videos.encode_started_at` in migration 78.
- [x] 1.2 Claim the lease in `NormalizeVideoAsync` and `TranscodeWebMAsync` before the download; skip while another conversion holds it. Release it on publish and on a recorded failure. Test the claim and the release, the lease against PostgreSQL.

## 2. Faster playback encodes

- [x] 2.1 Share one set of H.264 playback options across normalize, transcode, trim and composite: `-preset veryfast` and `-fpsmax 60` in place of `-preset fast` and `-r 60`. Test that no builder forces a frame rate.

## 3. Verification

- [x] 3.1 Run the Go checks.
- [x] 3.2 Benchmark the old and new encode on a 10-minute synthetic recording on a staging task the size of production. The old encode took 530 and 529 s, the new one 208 and 211 s.
- [ ] 3.3 After the production release, read `normalize: starting` and `normalize: completed` in production logs and confirm one encode per recording.
