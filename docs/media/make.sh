#!/usr/bin/env bash
# Records the README's screenshots and the trailer from the demo shop in demo/shop.
#
# Needs Docker, a JDK, Go, VHS (brew install vhs, which brings ttyd and ffmpeg), Google Chrome, and Hack Nerd Font.
# The demo runs with its own HOME, cache and Gradle home in a work directory, so your settings, run history and
# Gradle setup stay out of the pictures. Usage: docs/media/make.sh [stills] [trailer] (both when given neither)
set -euo pipefail
media=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$media/../.." && pwd)
work=${TESTDASH_MEDIA_WORK:-${TMPDIR:-/tmp}/testdash-media}
targets=("$@")
[[ ${#targets[@]} -gt 0 ]] || targets=(stills trailer)
export PATH=/opt/homebrew/bin:$PATH
shop=(docker compose --file "$repo/demo/shop/compose.yaml")
# The demo's containers are one Compose project, whoever runs it: a run here would replace those of a run elsewhere
if [[ -n $("${shop[@]}" ps --quiet 2>/dev/null) ]]; then
  echo "The demo is running; stop it first (docker compose down, in demo/shop)." >&2
  exit 1
fi
trap '"${shop[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true' EXIT

mkdir -p "$work/bin" "$work/home/.config/testdash" "$work/gradle/wrapper" "$work/tapes/shots"
rm -rf "$work/cache/testdash/history" # each recording starts from the runs warmup makes
(cd "$repo" && go build -o "$work/bin/testdash" .)
real_gradle=${GRADLE_USER_HOME:-$HOME/.gradle}
[[ -e "$work/gradle/wrapper/dists" ]] || ln -s "$real_gradle/wrapper/dists" "$work/gradle/wrapper/dists"
# testdash compiles its JUnit listener against the JUnit jars in Gradle's cache, which the demo's Gradle home shares
modules="$work/gradle/caches/modules-2"
mkdir -p "$work/gradle/caches"
[[ -L $modules ]] || { rm -rf "$modules" && ln -s "$real_gradle/caches/modules-2" "$modules"; }
cp "$media"/tapes/*.tape "$work/tapes/"

# setup.tape starts each recording's shell; its arguments are more variables to export, such as TESTDASH_KEYCAST=1
setup() {
  cat > "$work/tapes/setup.tape" <<TAPE
Hide
Type@2ms "export COLORTERM=truecolor DOCKER_CONFIG=${DOCKER_CONFIG:-$HOME/.docker} XDG_CACHE_HOME=$work/cache GRADLE_USER_HOME=$work/gradle HOME=$work/home PATH=$work/bin:\$PATH $*; unset XDG_CONFIG_HOME; cd $repo/demo/shop; PROMPT='%F{magenta}~/shop%f %F{13}❯%f '; clear"
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

stills=(running pick failure traces wiremock kafka logs sql redis noise live activity classes history runs settings)
scenes=(scene1 scene2 scene3 scene4 scene5 scene6 scene7)

setup
record warmup # lists the demo's tests, starts Gradle's daemon and runs the tests once, to have a run before

# Everything is recorded before anything is written into the repository, which would show as changes in the runs
for target in "${targets[@]}"; do
  case $target in
    stills) setup && record stills ;;
    trailer) setup TESTDASH_KEYCAST=shortcuts && record "${scenes[@]}" ;; # the trailer shows each shortcut pressed
    *) echo "unknown target $target: stills or trailer" >&2; exit 2 ;;
  esac
done

for target in "${targets[@]}"; do
  case $target in
    stills)
      "$media/compose/frame-stills.sh" "$work/tapes/shots" "$repo/docs/images" "${stills[@]}"
      ;;
    trailer)
      "$media/compose/compose.sh" "$work/tapes" "$repo/docs/trailer.mp4"
      seconds=$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$repo/docs/trailer.mp4")
      length=$(python3 -c "s = round($seconds); print(f'{s // 60}:{s % 60:02d}')")
      "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --headless --disable-gpu --hide-scrollbars \
        --force-device-scale-factor=1 --window-size=1920,1080 --screenshot="$work/poster.png" \
        "file://$media/compose/card.html?poster=1&length=$length" >/dev/null 2>&1
      ffmpeg -v error -y -i "$work/poster.png" -vf "scale=1280:-1:flags=lanczos" "$repo/docs/images/trailer.png"
      ;;
  esac
done
