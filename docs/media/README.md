# Screenshots and trailer

The README's screenshots (`docs/images`) and the trailer (`docs/trailer.mp4`) are recorded from the demo shop in
`demo/shop`, so they can be made again when the screen changes:

```sh
docs/media/make.sh            # both
docs/media/make.sh stills     # the screenshots only
docs/media/make.sh trailer    # the trailer only
```

It needs Docker, a JDK, Go, [VHS](https://github.com/charmbracelet/vhs) (`brew install vhs`, which brings ttyd and
ffmpeg), Google Chrome and [Hack Nerd Font](https://www.nerdfonts.com). The demo runs with its own `HOME` and Gradle
home under `$TMPDIR/testdash-media`, so your settings stay out of the pictures.

- `tapes/` are the VHS scripts: `stills.tape` walks through every screen taking screenshots, and `scene1` to
  `scene5` are the trailer's scenes. They type keys, so they follow the demo's test order and services. The
  scenes run testdash with `TESTDASH_KEYCAST=shortcuts`, which shows each shortcut pressed and what it did.
- `compose/` frames the screenshots in a window (`window.html`), and builds the trailer from the scenes on a
  captioned backdrop (`scene.html`) between a title and an end card (`card.html`), with headless Chrome and ffmpeg.
