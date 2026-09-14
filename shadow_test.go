package main

import (
	"os"
	"os/exec"
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
	if log, err := s.Log("", "", 0); err != nil || len(log) != 0 {
		t.Fatalf("empty repo: log=%v err=%v", log, err)
	}

	write := func(content string) {
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	snap := func() string {
		sha, err := s.Snapshot("")
		if err != nil || sha == "" {
			t.Fatalf("snapshot: sha=%q err=%v", sha, err)
		}
		return sha
	}

	write("hello\n")
	first := snap() // initial
	write("hello\nworld\n")
	snap()
	if sha, err := s.Snapshot(""); err != nil || sha != "" {
		t.Fatalf("unchanged tree should not commit: sha=%q err=%v", sha, err)
	}
	write("hello\nbye\n")
	last := snap()

	log, err := s.Log("", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 2 { // initial snapshot is the baseline, hidden
		t.Fatalf("want 2 entries, got %d: %+v", len(log), log)
	}
	if log[0].SHA != last || log[0].Files != 1 || log[0].Added != 1 || log[0].Deleted != 1 {
		t.Errorf("last entry: %+v", log[0])
	}
	if log[1].SHA == first {
		t.Errorf("initial snapshot should be hidden: %+v", log)
	}
	if after, _ := s.Log(log[1].SHA, "", 0); len(after) != 1 || after[0].SHA != last {
		t.Errorf("log after second: %+v", after)
	}
	if page, _ := s.Log("", "", 1); len(page) != 1 || page[0].SHA != last {
		t.Errorf("first page of 1: %+v", page)
	}
	if older, _ := s.Log("", last, 1); len(older) != 1 || older[0].SHA != log[1].SHA {
		t.Errorf("page before last: %+v", older)
	}
	if none, err := s.Log("", log[1].SHA, 1); err != nil || len(none) != 0 {
		t.Errorf("page before oldest visible should be empty: %+v %v", none, err)
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

	// a project commit: no tree change, but a marker snapshot with a message
	if h := s.ProjectHead(); h != "" {
		t.Fatalf("not a git repo yet, got head %q", h)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "Add greeting"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	head := s.ProjectHead()
	if !strings.HasSuffix(head, " Add greeting") {
		t.Fatalf("project head: %q", head)
	}
	mark, err := s.Snapshot(head)
	if err != nil || mark == "" {
		t.Fatalf("marker snapshot: %q %v", mark, err)
	}
	log, _ = s.Log("", "", 1)
	if log[0].SHA != mark || log[0].Message != head || log[0].Files != 0 {
		t.Errorf("marker entry: %+v", log[0])
	}
}
