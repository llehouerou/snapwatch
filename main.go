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

	snap := func() {
		sha, err := shadow.Snapshot()
		if err != nil {
			log.Println("snapshot:", err)
			return
		}
		if sha != "" {
			log.Println("snapshot", sha[:8])
			sv.Broadcast(sha)
		}
	}
	snap()
	go func() { log.Fatal(Watch(shadow.WorkTree, *debounce, snap)) }()

	url := "http://" + *addr
	log.Println("listening on", url)
	if *open {
		_ = exec.Command("xdg-open", url).Start()
	}
	log.Fatal(http.ListenAndServe(*addr, sv.Handler()))
}
