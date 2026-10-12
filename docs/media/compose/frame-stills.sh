#!/usr/bin/env bash
# Puts each still in a window frame on a transparent background, for the README: frame-stills.sh <shots dir> <out dir> <name>...
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
shots=$1 out=$2
shift 2
framed=$(mktemp -d)
trap 'rm -rf "$framed"' EXIT
mkdir -p "$out"
export PATH=/opt/homebrew/bin:$PATH
for name in "$@"; do
  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --headless --disable-gpu --hide-scrollbars \
    --force-device-scale-factor=1 --default-background-color=00000000 --window-size=2464,1480 \
    --screenshot="$framed/$name.png" "file://$here/window.html?img=$shots/$name.png&title=shop%20%E2%80%94%20testdash" \
    >/dev/null 2>&1
  ffmpeg -v error -y -i "$framed/$name.png" -vf "scale=1760:-1:flags=lanczos" -pix_fmt rgba "$out/$name.png"
  echo "$name: $(du -k "$out/$name.png" | cut -f1)KB"
done
