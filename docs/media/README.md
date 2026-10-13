# Screenshots and trailer

The README's screenshots (`docs/images`) and the trailer (`docs/trailer.mp4`) are recorded from the demo shop in
`demo/shop`, so they can be made again when the screen changes:

```sh
docs/media/make.sh            # both
docs/media/make.sh stills     # the screenshots only
docs/media/make.sh trailer    # the trailer only
```

It needs Docker, a JDK, Go, [VHS](https://github.com/charmbracelet/vhs) (`brew install vhs`, which brings ttyd and
ffmpeg), Google Chrome and [Hack Nerd Font](https://www.nerdfonts.com). The demo runs with its own `HOME`, cache and
Gradle home under `$TMPDIR/testdash-media`, as a fresh checkout would, so your settings, run history and Gradle
setup stay out of the pictures and nothing touches them. The first time, Gradle fetches itself and the demo's
dependencies from Maven Central. Stop the demo
first if it is running: the recordings use the same Compose project.

- `tapes/` are the VHS scripts: `warmup.tape` runs the tests once, so there is a run before; `stills.tape` walks
  through every screen taking screenshots, and `scene1` to `scene7` are the trailer's scenes; only the first
  starts from the shell. They type keys, so they follow the demo's test order and services. The scenes run
  testdash with `TESTDASH_KEYCAST=shortcuts`, which shows each shortcut pressed and what it did. Everything is recorded before anything is written into the
  repository, so the runs' history shows a clean working tree.
- `compose/` frames the screenshots in a window (`window.html`), and builds the trailer from the scenes on a
  captioned backdrop (`scene.html`) between a title and an end card (`card.html`), with headless Chrome and ffmpeg.

GitHub plays a video in a README only when it was uploaded to GitHub, not from a file in the repository. After
recording the trailer, drag `docs/trailer.mp4` into any comment box on github.com (it need not be posted), and
put the `https://github.com/user-attachments/assets/...` link it gives in the README, on a line of its own, in
place of the old one.
