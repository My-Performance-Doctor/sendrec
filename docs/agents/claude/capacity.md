# Capacity: transcription, encodes and time to patient

## What bounds time to patient

- mpd-api makes no report until SendRec's transcript exists (`waiting_captions` in `mpd-api/application/src/video_report/sendrec/processing.py`). Only `video.transcript_ready` moves it on; a failed transcript leaves it waiting forever.
- Staff OS polls the recording until `done` or `dead` (`src/features/videos/recording-store.ts`, `sendTake`). Before staff-os `fix/videos-wait-for-long-processing` it failed the take at 10 minutes with "Processing is taking too long."
- Finalize marks a screen recording `ready` at once, starts normalize in-process (`EnqueueJob` goroutine) and queues the transcript. Staff OS uploads are `avc3`, so every one re-encodes.
- Transcription: one job per task, deadline `transcriptionTimeout(duration)` = 30 min + 4×duration (extra capped at 4 h), failure → `failed`, no retry. Whisper runs at `nice -n 19` (`whisperCommand`), and the worker skips the claim while `encoders().busy()`.
- Encode deadlines start before `encoders().acquire`, so queueing for the slot spends the encode's budget. Do not make Whisper take the encoder slot without fixing that first.

## Measured on 2 vCPU / 4 GB Fargate (staging)

10-minute synthetic 2940×1484 screen + camera bubble, looped speech, exact production arguments (`scripts/capacity-bench.sh`):

| Mode | Result |
| --- | --- |
| Whisper alone | 605 s (1.0× the recording) |
| Encode alone | 448 s |
| Both, same priority (slow host) | encode 1108 s, Whisper 2207 s (3.7×, over the old 30-minute deadline) |
| Both, Whisper at nice 19, normal host | encode 566 s, Whisper 1201 s |
| Both, Whisper at nice 19, slow host | encode 959 s, Whisper 2181 s |

At nice 19 the encode is barely slowed and Whisper effectively waits for it, so Whisper ≈ encode + Whisper alone. On a slow host that still means about 36 minutes for a 10-minute recording. The deadline (70 minutes there) is what keeps it from failing; larger tasks are what would make it faster.

Hosts vary about 2× (source generation took 317–668 s on identical tasks). Whisper's ggml threads wait at barriers, so CPU contention slows it far more than the encode. Staging logs show dense synthetic speech at about 2.4× even alone (90 s → 218 s).

## Fast paths

```bash
# Whisper timings from logs (production: AWS_PROFILE=mpd-prod, /ecs/sendrec-production)
aws logs start-query --log-group-name /ecs/sendrec-staging --start-time $(( $(date +%s)-30*86400 )) --end-time $(date +%s) \
  --query-string 'fields @timestamp,@message | filter @message like /transcribe: starting|transcribe: completed/ | sort @timestamp asc'
# One-off benchmark task. Take subnets and security groups from the service:
aws ecs describe-services --cluster sendrec-staging-cluster --services sendrec-staging-service --query 'services[0].networkConfiguration'
aws ecs run-task --cluster sendrec-staging-cluster --task-definition sendrec-staging:<n> --launch-type FARGATE --started-by capacity-bench \
  --network-configuration 'awsvpcConfiguration={subnets=[...],securityGroups=[...],assignPublicIp=DISABLED}' \
  --overrides "$(python3 -c 'import json,sys;print(json.dumps({"containerOverrides":[{"name":"sendrec","command":["sh","-c",open(sys.argv[1]).read()],"environment":[{"name":"MODE","value":"nice"},{"name":"DUR","value":"600"}]}]}))' docs/agents/claude/scripts/capacity-bench.sh)"
# Results: aws logs filter-log-events --log-group-name /ecs/sendrec-staging --filter-pattern '"BENCH"'
```

The entrypoint downloads the Whisper model, then `exec "$@"` runs the override; the app never starts. Busybox `nice` is in the Alpine runtime image and works as the `sendrec` user (`nice` prints 19).
