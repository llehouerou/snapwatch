# snapwatch

Watch a directory, snapshot every change into a shadow git repo, and browse the
history of diffs live in your browser. Made for watching what an LLM agent does
to a project — versioned or not. The project's own `.git` is never touched.

## Install

    nix run github:llehouerou/snapwatch -- <dir>      # or: nix profile install github:llehouerou/snapwatch
    go install github.com/llehouerou/snapwatch@latest  # needs git in PATH

## Usage

    snapwatch [--addr 127.0.0.1:7777] [--debounce 300ms] [--open=false] <dir>

Opens `http://127.0.0.1:7777`. Snapshots live in `$XDG_DATA_HOME/snapwatch/<hash>/`;
delete that directory to start over.

- **Feed** — every snapshot, newest first, as side-by-side diffs under a sticky
  timestamp; infinite scroll. Files with more than 200 changed lines start folded.
- **Git markers** — if the project is a git repo, its commits (`● sha subject`)
  and branch switches (`⎇ switched to …`) appear in the feed. Files git touched
  on a checkout/pull are folded away instead of shown as changes.
- **History sidebar** — the working tree's pending changes, then the last 100
  project commits, each with its files as a tree. Click a file to see its diff;
  `Esc` or `← feed` returns to the feed, `h` hides the sidebar.
- **⚙ Settings** — colour theme (all Chroma styles, Catppuccin included), font
  (installed fonts listed on Chromium), size. Stored in the browser.

## Demo

    snapwatch /tmp/demo            # terminal 1
    echo x >> /tmp/demo/a.txt      # terminal 2 — the feed updates without reloading

## Hacking

    watchexec -r -- go run . /tmp/demo   # in `nix develop`; the page reloads itself on restart
