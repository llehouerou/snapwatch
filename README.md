# snapwatch

Watch a directory, snapshot every change into a shadow git repo, and browse the
history of diffs live in your browser. Made for watching what an LLM agent does
to a project — versioned or not. The project's own `.git` is never touched.

## Install

    nix run github:llehouerou/snapwatch -- <dir>      # or: nix profile install github:llehouerou/snapwatch
    go install github.com/llehouerou/snapwatch@latest  # needs git in PATH

## Usage

    snapwatch [--addr 127.0.0.1:7777] [--debounce 300ms] [--open=false] <dir>

Opens `http://127.0.0.1:7777`. Click a snapshot for its diff, shift-click a
second one for the cumulative diff. Snapshots live in
`$XDG_DATA_HOME/snapwatch/<hash>/`.

## Demo

    snapwatch /tmp/demo            # terminal 1
    echo x >> /tmp/demo/a.txt      # terminal 2 — the timeline updates without reloading

## Hacking

    watchexec -r -- go run . /tmp/demo   # in `nix develop`; the page reloads itself on restart
