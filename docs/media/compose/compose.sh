#!/usr/bin/env bash
# Builds the trailer from the scene recordings: each in a window on a captioned backdrop, between a title card
# and an end card, joined with crossfades. Usage: compose.sh <dir with scene1.mp4...scene6.mp4> <trailer.mp4>
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
vhs=$1
trailer=$2
out=$(mktemp -d)
trap 'rm -rf "$out"' EXIT
export PATH=/opt/homebrew/bin:$PATH
chrome() {
  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --headless --disable-gpu --hide-scrollbars \
    --force-device-scale-factor=1 --window-size=1920,1080 --screenshot="$1" "$2" >/dev/null 2>&1
}
urlencode() { python3 -c 'import sys, urllib.parse; print(urllib.parse.quote(sys.argv[1]))' "$1"; }

captions=(
  "Pick the tests to run|Every class, nested class and parameterised test, and what failed last time"
  "Watch the run, live|Progress, then everything every service does, in one feed and on a rolling chart"
  "See why a test failed|Every request, query, message and log line while it ran, grouped by trace"
  "Follow every service|Your app's logs, WireMock, Kafka, MariaDB, MongoDB, Postgres and Redis"
  "Cut the noise|Hide the chatter with a rule, seeing what it matches as you type"
  "Remember every run|What failed before, for how long, and the runs to run again"
)
speed=(1 1.8 1 1 1 1) # the run itself plays faster
scenes=${#captions[@]}

fps=30
fade=0.4
encode=(-c:v libx264 -preset slow -crf 16 -pix_fmt yuv420p -r $fps)

chrome "$out/title.png" "file://$here/card.html"
chrome "$out/end.png" "file://$here/card.html?end=1"
ffmpeg -v error -y -loop 1 -t 2.4 -i "$out/title.png" -vf "fade=in:st=0:d=0.5" "${encode[@]}" "$out/part0.mp4"

for ((i = 0; i < scenes; i++)); do
  n=$((i + 1))
  title=${captions[$i]%%|*}
  sub=${captions[$i]#*|}
  chrome "$out/scene$n.png" "file://$here/scene.html?step=0$n&title=$(urlencode "$title")&sub=$(urlencode "$sub")"
  ffmpeg -v error -y -loop 1 -i "$out/scene$n.png" -i "$vhs/scene$n.mp4" -filter_complex \
    "[1:v]setpts=PTS/${speed[$i]},fps=$fps[term];[0:v][term]overlay=152:214:shortest=1" \
    "${encode[@]}" -an "$out/part$n.mp4"
done
last=$((scenes + 1))
ffmpeg -v error -y -loop 1 -t 5 -i "$out/end.png" "${encode[@]}" "$out/part$last.mp4" # it stays, to read when it stops

inputs=()
for ((n = 0; n <= last; n++)); do inputs+=(-i "$out/part$n.mp4"); done
filter=""
offset=0
previous="0:v"
for ((n = 1; n <= last; n++)); do
  duration=$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$out/part$((n - 1)).mp4")
  offset=$(python3 -c "print(round($offset + $duration - $fade, 3))")
  label="v$n"
  filter+="[$previous][$n:v]xfade=transition=fade:duration=$fade:offset=$offset[$label];"
  previous=$label
done
ffmpeg -v error -y "${inputs[@]}" -filter_complex "${filter%;}" -map "[$previous]" \
  -c:v libx264 -preset slow -crf 18 -pix_fmt yuv420p -r $fps -movflags +faststart "$trailer"
ffprobe -v error -show_entries format=duration,size -of default=nw=1 "$trailer"
