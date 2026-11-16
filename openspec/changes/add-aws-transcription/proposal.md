## Why

mpd-api makes a patient's report only once SendRec's transcript exists. Local Whisper (`ggml-small`, two threads on a 2 vCPU task) takes 1 to 3.6 times a recording's length, and it waits while the task encodes. The first production recording's transcript ran into the 30-minute limit and was killed; a manual re-transcription took 20 minutes.

On 17 November 2026 the same audio went through both systems:

| Test | Whisper small (production model and flags) | Amazon Transcribe batch, `en-AU`, Sydney |
| --- | --- | --- |
| 10.6-minute synthetic monologue | 10–36 min on a 2 vCPU task (earlier benchmarks) | 18 s |
| PriMock57, 10 doctor tracks, 92 min of real mock consultations | WER 13.4% | WER 7.2% |
| Same, each track | 6.5–12.4 min audio | 12–16 s, all ten at once |

WER is after Whisper's English normalizer. Transcribe was better on all ten tracks and missed about half as many long clinical words (2.5% against 4.8%). Whisper dropped "allergic to amoxicillin" and wrote "amolions" for "emollients". Transcribe batch in Sydney costs US$0.0001 a second, about US$0.06 for a 10-minute recording.

The audio stays in MPD's AWS account and Sydney region. On 17 November MPD's AWS organization attached an AI services opt-out policy to its root, so Transcribe may not store or use the audio to improve the service.

## What Changes

- A transcription provider `aws` (`TRANSCRIPTION_PROVIDER=aws`). It uploads the extracted 16 kHz mono WAV under `transcribe/` in the recordings bucket, runs a Transcribe batch job that writes its JSON to the same prefix, and turns the words into caption segments, closing a segment at a sentence end, after a pause of 1.5 seconds or at 10 seconds. It deletes the audio, the JSON and the job afterwards. `auto` and `en` use `TRANSCRIPTION_LANGUAGE_CODE` (default `en-AU`). A full locale such as `en-GB` is used as given, and any other language lets Transcribe identify it.
- With the `aws` provider, a worker claims a transcription even while its task encodes. Only local Whisper competes with encodes for the CPU.
- The recordings bucket expires everything under `transcribe/`, and every noncurrent version there, after one day. Production keeps every version of other objects, so without this rule a deleted temporary file would stay as a noncurrent version.
- The stack takes `transcriptionProvider` (`local` by default, or `aws`). With `aws` it sets the provider and language, and grants the task role `transcribe:StartTranscriptionJob`, `GetTranscriptionJob` and `DeleteTranscriptionJob` on jobs named `sendrec-*`. The task role already reads and writes the bucket, which Transcribe uses to read the audio and write its output.

Non-goals:

- Custom vocabulary for drug brands, and Transcribe Medical, which costs 12.5 times as much.
- Removing local Whisper. It stays the default and the fallback.
- Changing the transcription deadline or retrying failed transcripts.

## Capabilities

### New Capabilities

- `aws-transcription`: transcribing recordings with Amazon Transcribe.

### Modified Capabilities

None.

## Impact

The change touches `internal/video/transcriber_aws.go` (new), `transcriber_factory.go`, `transcribe_worker.go`, `cmd/sendrec/main.go`, the stack and their tests, and adds `github.com/aws/aws-sdk-go-v2/service/transcribe`. The lifecycle rule applies to both environments. Nothing changes until a deploy selects `transcriptionProvider=aws`.

## Approval and delivery

On 17 November 2026 Josh approved using Amazon Transcribe for patient audio and asked for this change. The AI services opt-out policy was attached the same day. Sami reviews this proposal and its implementation together in one pull request.
