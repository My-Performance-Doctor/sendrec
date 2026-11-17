# Encode benchmark: one synthetic Staff OS-shaped recording (1920x748 avc3, 30 fps, camera bubble, speech)
# through the old playback encode (-preset fast -r 60) and the new one (-preset veryfast -fpsmax 60).
# Run as a one-off staging task like capacity-bench.sh (app never starts; no DB or S3 writes). DUR=seconds.
set -eu
cd /tmp
log(){ echo "BENCH encode dur=$DUR $(date -u +%H:%M:%S) $*"; }
log start nproc=$(nproc)
wget -q -O jfk.wav https://raw.githubusercontent.com/ggml-org/whisper.cpp/v1.8.3/samples/jfk.wav
t=$(date +%s)
ffmpeg -nostdin -v error -f lavfi -i "testsrc=s=1920x748:r=30:d=$DUR" -f lavfi -i "testsrc2=s=320x240:r=30:d=$DUR" -stream_loop -1 -i jfk.wav \
  -filter_complex "[0:v][1:v]overlay=W-w-40:H-h-40[v]" -map "[v]" -map 2:a -t $DUR \
  -c:v libx264 -profile:v baseline -preset ultrafast -b:v 2500k -x264-params repeat-headers=1 -tag:v avc3 -c:a aac -b:a 96k src.mp4
log generated secs=$(( $(date +%s) - t )) bytes=$(stat -c %s src.mp4)
enc(){ label=$1; shift; s=$(date +%s)
  ffmpeg -nostdin -v error -filter_threads 4 -filter_complex_threads 4 -threads 4 -i src.mp4 \
    -c:v libx264 -profile:v high -level:v 5.1 "$@" -crf 23 \
    -vf "scale=1920:748:force_original_aspect_ratio=decrease:force_divisible_by=2,pad=1920:748:(ow-iw)/2:(oh-ih)/2" \
    -threads 4 -x264-params rc-lookahead=10:ref=1:bframes=0 -af arnndn=m=/app/models/std.rnnn -c:a aac -movflags +faststart -y out-$label.mp4 \
    && log encode-done label=$label secs=$(( $(date +%s) - s )) bytes=$(stat -c %s out-$label.mp4) \
    || log encode-FAILED label=$label secs=$(( $(date +%s) - s )); }
enc old -preset fast -r 60
enc new -preset veryfast -fpsmax 60
enc old2 -preset fast -r 60
enc new2 -preset veryfast -fpsmax 60
log end
