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

	// project git state: none, then a branch + commit, then a branch switch
	if b, h := s.ProjectHead(); b != "" || h != "" {
		t.Fatalf("not a git repo yet, got %q %q", b, h)
	}
	git := func(args ...string) {
		args = append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", ".")
	git("commit", "-qm", "Add greeting")
	branch, head := s.ProjectHead()
	if branch != "main" || !strings.HasSuffix(head, " Add greeting") {
		t.Fatalf("project state: %q %q", branch, head)
	}
	git("checkout", "-qb", "feature")
	if b, _ := s.ProjectHead(); b != "feature" {
		t.Errorf("after checkout: branch %q", b)
	}
	git("checkout", "-q", "--detach")
	if b, _ := s.ProjectHead(); b != "HEAD" {
		t.Errorf("detached: branch %q", b)
	}

	// a marker snapshot: always commits, even without tree changes
	mark, err := s.Snapshot("commit " + head)
	if err != nil || mark == "" {
		t.Fatalf("marker snapshot: %q %v", mark, err)
	}
	log, _ = s.Log("", "", 1)
	if log[0].SHA != mark || log[0].Kind != "commit" || log[0].Message != head || log[0].Files != 0 {
		t.Errorf("marker entry: %+v", log[0])
	}
}

func TestTree(t *testing.T) {
	root := Tree([]Change{
		{"M", "src/main/java/App.java"},
		{"A", "README.md"},
		{"M", "src/main/java/Util.java"},
		{"D", "src/test/Old.java"},
		{"A", "Makefile"},
	})
	if len(root.Dirs) != 1 || root.Dirs[0].Name != "src" {
		t.Fatalf("root dirs: %+v", root.Dirs)
	}
	if got := []string{root.Files[0].Name, root.Files[1].Name}; got[0] != "Makefile" || got[1] != "README.md" {
		t.Errorf("root files not sorted: %v", got)
	}
	src := root.Dirs[0]
	if len(src.Dirs) != 2 || src.Dirs[0].Name != "main/java" || src.Dirs[1].Name != "test" {
		t.Fatalf("src dirs (chain should be compacted): %v %v", src.Dirs[0].Name, src.Dirs[1].Name)
	}
	if f := src.Dirs[0].Files; len(f) != 2 || f[0].Name != "App.java" || f[0].Path != "src/main/java/App.java" || f[0].Status != "M" {
		t.Errorf("main/java files: %+v", f)
	}
}
