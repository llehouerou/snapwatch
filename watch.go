package main

import (
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// skipDirs mirrors the shadow repo's info/exclude for directories: we don't
// even watch them, so a `cargo build` doesn't flood the debounce timer.
var skipDirs = map[string]bool{".git": true, ".jj": true, "node_modules": true, "target": true, ".direnv": true}

// Watch recursively watches dir and calls snap after debounce of quiet time
// following the last event. Blocks forever; snap is never called concurrently.
func Watch(dir string, debounce time.Duration, snap func()) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()
	if err := addTree(w, dir); err != nil {
		return err
	}

	timer := time.NewTimer(0)
	<-timer.C
	for {
		select {
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			if ev.Has(fsnotify.Create) {
				if st, err := os.Stat(ev.Name); err == nil && st.IsDir() {
					_ = addTree(w, ev.Name)
				}
			}
			timer.Reset(debounce)
		case err := <-w.Errors:
			log.Println("watch:", err)
		case <-timer.C:
			snap()
		}
	}
}

func addTree(w *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if p != root && skipDirs[d.Name()] {
			return filepath.SkipDir
		}
		return w.Add(p)
	})
}
