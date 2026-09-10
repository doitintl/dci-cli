# Demo recordings

Scripted terminal recordings of the CLI for internal "what's new" videos.
Each section is a [VHS](https://github.com/charmbracelet/vhs) tape in
`tapes/`; `record.sh` records them against the live API, puts a title slide
before each section, and stitches a 4K master plus a 1080p copy into `out/`.

```bash
brew install vhs ffmpeg jq      # uv renders the slides (demo/slides.py)
dci login                        # as a Doer, customer context doit.com
demo/record.sh                   # ~20 minutes; sections run one at a time
```

Conventions that keep the tapes reliable:

- `tapes/settings.tape` holds the shared look (Menlo 36px at 3840x2160, the
  same 153x41 cell layout as Menlo 18px at 1080p) and a hidden setup line
  and `tapes/setup.tape` the hidden setup line that forces human mode and a
  `doit.com ❯` prompt. A section that needs its own `Set` (the update
  section's longer wait timeout) puts it between the two `Source` lines,
  because VHS ignores `Set` directives that follow a command.
- Every section waits on screen content, never on a fixed sleep, because API
  latency varies: `Wait` blocks until the prompt returns and `Wait+Screen`
  until a pattern that only the command's output contains. Do not use a
  pattern that also appears in the typed command line.
- `--chart` takes its value with `=` (`--chart=treemap`); a space makes the
  mode part of the report name.
- Typed text must be ASCII: VHS garbles non-ASCII keystrokes. The demo
  report is named with a plain hyphen for that reason.
- Section 10 (`dci update`) is a real upgrade, so it records first and only
  when the installed CLI is behind the latest release; to re-record it,
  reinstall the previous release first.
- Every section ends on a 3 s hold so viewers can take the output in
  before the next title slide.
- Section 8 uploads into the `cli-demo-import` dataset, created on demand;
  `record.sh --cleanup` deletes it.
- Slide copy lives in `slides.py`; edit `SLIDES` and re-run
  `record.sh --stitch` to re-cut without re-recording.
