# Capacity benchmark: one synthetic recording through the exact production encode and Whisper commands.
# Run as a one-off staging task (app never starts; no DB or S3 writes). MODE=whisper|encode|both|nice (both, Whisper at nice 19), DUR=seconds.
# See docs/agents/claude/capacity.md for the run-task command and results.
set -eu
cd /tmp
log(){ echo "BENCH mode=$MODE dur=$DUR $(date -u +%H:%M:%S) $*"; }
N=""; [ "$MODE" = nice ] && N="nice -n 19"
log start nproc=$(nproc) nice="$N" niceness=$($N nice 2>&1 || echo none) user=$(id -un)
wget -q -O jfk.wav https://raw.githubusercontent.com/ggml-org/whisper.cpp/v1.8.3/samples/jfk.wav
t=$(date +%s)
ffmpeg -nostdin -v error -f lavfi -i "testsrc=s=2940x1484:r=30:d=$DUR" -f lavfi -i "testsrc2=s=480x360:r=30:d=$DUR" -stream_loop -1 -i jfk.wav -filter_complex "[0:v][1:v]overlay=W-w-40:H-h-40[v]" -map "[v]" -map 2:a -t $DUR -c:v libx264 -preset ultrafast -crf 20 -pix_fmt yuv420p -c:a aac -b:a 96k src.mp4
log generated secs=$(( $(date +%s) - t )) bytes=$(stat -c %s src.mp4)
enc(){ s=$(date +%s); ffmpeg -nostdin -v error -filter_threads 4 -filter_complex_threads 4 -threads 4 -i src.mp4 -c:v libx264 -profile:v high -level:v 5.1 -preset fast -crf 23 -vf "scale=1920:968:force_original_aspect_ratio=decrease:force_divisible_by=2,pad=1920:968:(ow-iw)/2:(oh-ih)/2" -r 60 -threads 4 -x264-params rc-lookahead=10:ref=1:bframes=0 -af arnndn=m=/app/models/std.rnnn -c:a aac -movflags +faststart -y out.mp4 && log encode-done secs=$(( $(date +%s) - s )) bytes=$(stat -c %s out.mp4) || log encode-FAILED secs=$(( $(date +%s) - s )); }
wsp(){ s=$(date +%s); $N ffmpeg -nostdin -v error -filter_threads 4 -filter_complex_threads 4 -threads 4 -i src.mp4 -ar 16000 -ac 1 -c:a pcm_s16le -y audio.wav; $N whisper-cli -m "$WHISPER_MODEL_PATH" -f audio.wav --output-json -of wout -t 2 -l auto >/dev/null 2>&1 && log whisper-done secs=$(( $(date +%s) - s )) segments=$(grep -c '"text"' wout.json) || log whisper-FAILED secs=$(( $(date +%s) - s )); }
case "$MODE" in
  whisper) wsp ;;
  encode) enc ;;
  both|nice) enc & wsp; wait ;;
esac
log end
