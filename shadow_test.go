package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShadow(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir := t.TempDir()
	s, err := OpenShadow(dir)
	if err != nil {
		t.Fatal(err)
	}
	if log, err := s.Log(""); err != nil || len(log) != 0 {
		t.Fatalf("empty repo: log=%v err=%v", log, err)
	}

	write := func(content string) {
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	snap := func() string {
		sha, err := s.Snapshot()
		if err != nil || sha == "" {
			t.Fatalf("snapshot: sha=%q err=%v", sha, err)
		}
		return sha
	}

	write("hello\n")
	first := snap() // initial
	write("hello\nworld\n")
	snap()
	if sha, err := s.Snapshot(); err != nil || sha != "" {
		t.Fatalf("unchanged tree should not commit: sha=%q err=%v", sha, err)
	}
	write("hello\nbye\n")
	last := snap()

	log, err := s.Log("")
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 3 {
		t.Fatalf("want 3 entries, got %d: %+v", len(log), log)
	}
	if log[0].SHA != last || log[0].Files != 1 || log[0].Added != 1 || log[0].Deleted != 1 {
		t.Errorf("last entry: %+v", log[0])
	}
	if log[2].SHA != first || log[2].Added != 1 {
		t.Errorf("first entry: %+v", log[2])
	}
	if after, _ := s.Log(first); len(after) != 2 {
		t.Errorf("log after first: want 2, got %d", len(after))
	}

	d, err := s.Diff("", last)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d, "-world") || !strings.Contains(d, "+bye") {
		t.Errorf("diff of last:\n%s", d)
	}
	d, _ = s.Diff(first, last)
	if !strings.Contains(d, "+bye") || strings.Contains(d, "world") {
		t.Errorf("cumulative diff:\n%s", d)
	}
}
