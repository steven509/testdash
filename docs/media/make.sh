#!/usr/bin/env bash
# Records the README's screenshots and the trailer from the demo shop in demo/shop.
#
# Needs Docker, a JDK, Go, VHS (brew install vhs, which brings ttyd and ffmpeg), Google Chrome, and Hack Nerd Font.
# The demo runs with its own HOME and Gradle home in a work directory, so your settings and Gradle setup stay
# out of the pictures. Usage: docs/media/make.sh [stills] [trailer] (both when given neither)
set -euo pipefail
media=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$media/../.." && pwd)
work=${TESTDASH_MEDIA_WORK:-${TMPDIR:-/tmp}/testdash-media}
targets=("$@")
[[ ${#targets[@]} -gt 0 ]] || targets=(stills trailer)
export PATH=/opt/homebrew/bin:$PATH

mkdir -p "$work/bin" "$work/home/.config/testdash" "$work/gradle/wrapper" "$work/tapes/shots"
(cd "$repo" && go build -o "$work/bin/testdash" .)
"$repo/demo/shop/libs.sh"
real_gradle=${GRADLE_USER_HOME:-$HOME/.gradle}
[[ -e "$work/gradle/wrapper/dists" ]] || ln -s "$real_gradle/wrapper/dists" "$work/gradle/wrapper/dists"
# testdash compiles its JUnit listener against the JUnit jars in ~/.gradle, which the demo's HOME has to reach
[[ -e "$work/home/.gradle" ]] || ln -s "$real_gradle" "$work/home/.gradle"
cp "$media"/tapes/*.tape "$work/tapes/"

# setup.tape starts each recording's shell; its arguments are more variables to export, such as TESTDASH_KEYCAST=1
setup() {
  cat > "$work/tapes/setup.tape" <<TAPE
Hide
Type@2ms "export COLORTERM=truecolor DOCKER_CONFIG=${DOCKER_CONFIG:-$HOME/.docker} XDG_CACHE_HOME=${XDG_CACHE_HOME:-$HOME/.cache} GRADLE_USER_HOME=$work/gradle HOME=$work/home PATH=$work/bin:\$PATH $*; unset XDG_CONFIG_HOME; cd $repo/demo/shop; PROMPT='%F{magenta}~/shop%f %F{13}❯%f '; clear"
Enter
Sleep 500ms
TAPE
}

record() {
  for tape in "$@"; do
    printf '{"nerdFont":true,"iconSpacing":1,"theme":"Violet","mouse":true}\n' > "$work/home/.config/testdash/settings.json"
    echo "recording $tape"
    (cd "$work/tapes" && vhs "$tape.tape" > "$tape.log" 2>&1) || { cat "$work/tapes/$tape.log"; exit 1; }
  done
}

setup
record warmup # lists the demo's tests and starts Gradle's daemon, so the recordings never wait for them

for target in "${targets[@]}"; do
  case $target in
    stills)
      setup
      record stills
      "$media/compose/frame-stills.sh" "$work/tapes/shots" "$repo/docs/images" \
        running pick failure wiremock kafka logs settings
      ;;
    trailer)
      setup TESTDASH_KEYCAST=shortcuts # the trailer shows each shortcut pressed
      record scene1 scene2 scene3 scene4 scene5
      "$media/compose/compose.sh" "$work/tapes" "$repo/docs/trailer.mp4"
      seconds=$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$repo/docs/trailer.mp4")
      length=$(python3 -c "s = round($seconds); print(f'{s // 60}:{s % 60:02d}')")
      "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --headless --disable-gpu --hide-scrollbars \
        --force-device-scale-factor=1 --window-size=1920,1080 --screenshot="$work/poster.png" \
        "file://$media/compose/card.html?poster=1&length=$length" >/dev/null 2>&1
      ffmpeg -v error -y -i "$work/poster.png" -vf "scale=1280:-1:flags=lanczos" "$repo/docs/images/trailer.png"
      ;;
    *) echo "unknown target $target: stills or trailer" >&2; exit 2 ;;
  esac
done
(cd "$repo/demo/shop" && docker compose down --volumes --remove-orphans >/dev/null 2>&1) || true
