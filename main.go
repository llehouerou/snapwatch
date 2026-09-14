package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:7777", "listen address")
	debounce := flag.Duration("debounce", 300*time.Millisecond, "quiet time before a snapshot")
	open := flag.Bool("open", true, "open the browser at startup")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: snapwatch [flags] <dir>\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}

	shadow, err := OpenShadow(flag.Arg(0))
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("watching %s (shadow repo %s)", shadow.WorkTree, shadow.GitDir)
	sv := NewServer(shadow)

	// When the project's own git state moved since the previous snapshot, the
	// snapshot is a marker: "branch <name>" on a switch, else "commit <sha>
	// <subject>". A checkout's diff is git's doing, so the UI folds it; a
	// commit's diff is real work done in the same window and stays visible.
	// ponytail: a pull/reset on the same branch shows as work too; compare the
	// new HEAD's committer time with now if that gets annoying.
	branch, head := shadow.ProjectHead()
	snap := func() {
		message := ""
		b, h := shadow.ProjectHead()
		switch {
		case b != branch:
			message = "branch " + b
		case h != head:
			message = "commit " + h
		}
		branch, head = b, h
		sha, err := shadow.Snapshot(message)
		if err != nil {
			log.Println("snapshot:", err)
			return
		}
		if sha != "" {
			log.Println("snapshot", sha[:8], message)
			sv.Broadcast(sha)
		}
	}
	snap()
	go func() { log.Fatal(Watch(shadow.WorkTree, *debounce, snap)) }()

	url := "http://" + *addr
	log.Println("listening on", url)
	if *open {
		// An already-open tab reconnects its SSE within ~500ms of a restart;
		// only spawn a new one when nobody shows up.
		time.AfterFunc(1500*time.Millisecond, func() {
			if !sv.Connected() {
				_ = exec.Command("xdg-open", url).Start()
			}
		})
	}
	log.Fatal(http.ListenAndServe(*addr, sv.Handler()))
}
