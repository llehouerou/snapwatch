package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const excludes = ".git/\n.jj/\nnode_modules/\ntarget/\nresult\n.direnv/\n*.log\n"

// Shadow is a bare-ish git repo living outside the watched directory, used as
// snapshot storage: --git-dir points at the shadow, --work-tree at the project.
type Shadow struct {
	GitDir   string
	WorkTree string
}

// Entry is one timeline row. Kind is "" for a plain snapshot, "commit" when
// the project's HEAD moved on the same branch (Message: "<sha> <subject>"),
// "branch" when it switched branch (Message: the new branch name).
type Entry struct {
	SHA     string
	Time    time.Time
	Kind    string
	Message string
	Files   int
	Added   int
	Deleted int
}

func OpenShadow(dir string) (*Shadow, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		data = filepath.Join(home, ".local", "share")
	}
	sum := sha256.Sum256([]byte(abs))
	s := &Shadow{GitDir: filepath.Join(data, "snapwatch", hex.EncodeToString(sum[:])[:16]), WorkTree: abs}

	if _, err := os.Stat(filepath.Join(s.GitDir, "HEAD")); err != nil {
		if err := os.MkdirAll(s.GitDir, 0o755); err != nil {
			return nil, err
		}
		if _, err := s.git("init", "-q"); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(filepath.Join(s.GitDir, "info", "exclude"), []byte(excludes), 0o644); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Shadow) git(args ...string) ([]byte, error) {
	base := []string{
		"--git-dir=" + s.GitDir, "--work-tree=" + s.WorkTree,
		"-c", "user.name=snapwatch", "-c", "user.email=snapwatch@local",
		"-c", "commit.gpgsign=false", "-c", "color.ui=false", "-c", "init.defaultBranch=main",
	}
	cmd := exec.Command("git", append(base, args...)...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// Snapshot stages everything and commits. With an empty message it only
// commits when something changed; with a message (a project commit marker)
// it always commits. Returns the new SHA, or "" when nothing was committed.
func (s *Shadow) Snapshot(message string) (string, error) {
	if _, err := s.git("add", "-A"); err != nil {
		return "", err
	}
	if message == "" {
		_, err := s.git("diff", "--cached", "--quiet")
		var exit *exec.ExitError
		switch {
		case err == nil:
			return "", nil // nothing staged
		case !errors.As(err, &exit) || exit.ExitCode() != 1:
			return "", err
		}
	}
	if _, err := s.git("commit", "-q", "--allow-empty", "--allow-empty-message", "-m", message); err != nil {
		return "", err
	}
	out, err := s.git("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// project runs git inside the watched project's own repository.
func (s *Shadow) project(args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", s.WorkTree, "-c", "core.quotepath=off", "-c", "color.ui=false"}, args...)...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// ProjectHead describes the watched project's own git state: the current
// branch ("HEAD" when detached) and "<short sha> <subject>" of its HEAD.
// Both are "" when the project is not a git repository or has no commits.
func (s *Shadow) ProjectHead() (branch, head string) {
	out, err := s.project("log", "-1", "--format=%h %s%n%D")
	if err != nil {
		return "", ""
	}
	head, refs, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	// %D looks like "HEAD -> main, origin/main" or just "HEAD" when detached
	branch = "HEAD"
	if _, b, ok := strings.Cut(refs, "HEAD -> "); ok {
		branch, _, _ = strings.Cut(b, ",")
	}
	return branch, head
}

// Change is one file touched by a commit or pending in the working tree.
// Status is git's letter: A, M, D, R (renamed), ? (untracked).
type Change struct {
	Status, Path string
}

// Commit is one entry of the project's own history with its files.
type Commit struct {
	SHA, Subject string
	Time         time.Time
	Files        []Change
}

// History returns the project's last n commits, newest first, with their
// files; nil when the project is not a git repository.
func (s *Shadow) History(n int) []Commit {
	out, err := s.project("log", "-n", strconv.Itoa(n), "-M", "--name-status", "--format=%x00%H%x00%ct%x00%s")
	if err != nil {
		return nil
	}
	var commits []Commit
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case line == "":
		case line[0] == 0:
			f := strings.Split(line, "\x00")
			ts, _ := strconv.ParseInt(f[2], 10, 64)
			commits = append(commits, Commit{SHA: f[1], Time: time.Unix(ts, 0), Subject: f[3]})
		case len(commits) > 0:
			// "M\tpath" or "R100\told\tnew"
			f := strings.Split(line, "\t")
			c := &commits[len(commits)-1]
			c.Files = append(c.Files, Change{Status: f[0][:1], Path: f[len(f)-1]})
		}
	}
	return commits
}

// Status lists the project's uncommitted changes (staged, unstaged, untracked).
// Node is a directory (Dirs/Files set) or a file (Status/Path set) of a
// change tree. Single-child directory chains are compacted ("src/main").
type Node struct {
	Name, Path, Status string
	Dirs, Files        []*Node
}

// Tree groups changes by directory, dirs first then files, both sorted.
func Tree(changes []Change) *Node {
	root := &Node{}
	for _, c := range changes {
		n := root
		parts := strings.Split(c.Path, "/")
		for _, dir := range parts[:len(parts)-1] {
			i := slices.IndexFunc(n.Dirs, func(d *Node) bool { return d.Name == dir })
			if i < 0 {
				n.Dirs = append(n.Dirs, &Node{Name: dir})
				i = len(n.Dirs) - 1
			}
			n = n.Dirs[i]
		}
		n.Files = append(n.Files, &Node{Name: parts[len(parts)-1], Path: c.Path, Status: c.Status})
	}
	var tidy func(*Node)
	tidy = func(n *Node) {
		for _, d := range n.Dirs {
			tidy(d)
		}
		for i, d := range n.Dirs {
			for len(d.Files) == 0 && len(d.Dirs) == 1 {
				d = d.Dirs[0]
				d.Name = n.Dirs[i].Name + "/" + d.Name
				n.Dirs[i] = d
			}
		}
		byName := func(a, b *Node) int { return strings.Compare(a.Name, b.Name) }
		slices.SortFunc(n.Dirs, byName)
		slices.SortFunc(n.Files, byName)
	}
	tidy(root)
	return root
}
func (s *Shadow) Status() []Change {
	out, err := s.project("status", "--porcelain=v1", "-uall")
	if err != nil {
		return nil
	}
	var changes []Change
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 4 {
			continue
		}
		// "XY path" or "R  old -> new"; X is the index status, Y the work tree
		st, path := strings.TrimSpace(line[:2]), line[3:]
		if _, newPath, ok := strings.Cut(path, " -> "); ok {
			path = newPath
		}
		changes = append(changes, Change{Status: st[:1], Path: path})
	}
	return changes
}

// FileDiff returns the unified diff of one file: in commit rev, or against
// HEAD in the working tree when rev is "" (untracked files diff from nothing).
func (s *Shadow) FileDiff(rev, path string) (string, error) {
	if rev != "" {
		out, err := s.project("show", "--format=", "-M", rev, "--", path)
		return string(out), err
	}
	out, err := s.project("diff", "HEAD", "--", path)
	if err == nil && len(out) == 0 {
		// not tracked: diff --no-index exits 1 when the file has content
		out, err = s.project("diff", "--no-index", "--", "/dev/null", path)
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			err = nil
		}
	}
	return string(out), err
}

// Log returns snapshots newest first: those newer than after, or up to n
// older than before (all when both are empty). The initial snapshot (root
// commit) is the baseline, not a change: skipped.
func (s *Shadow) Log(after, before string, n int) ([]Entry, error) {
	args := []string{"log", "--min-parents=1", "--format=%H%x00%ct%x00%s", "--shortstat"}
	if n > 0 {
		args = append(args, "-n", strconv.Itoa(n))
	}
	switch {
	case after != "":
		args = append(args, after+"..HEAD")
	case before != "":
		args = append(args, before+"~1")
	}
	out, err := s.git(args...)
	if err != nil {
		if strings.Contains(err.Error(), "does not have any commits") {
			return nil, nil
		}
		return nil, err
	}
	var entries []Entry
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.Contains(line, "\x00"):
			f := strings.Split(line, "\x00")
			ts, _ := strconv.ParseInt(f[1], 10, 64)
			kind, msg, _ := strings.Cut(f[2], " ")
			entries = append(entries, Entry{SHA: f[0], Time: time.Unix(ts, 0), Kind: kind, Message: msg})
		case len(entries) > 0:
			e := &entries[len(entries)-1]
			// " 3 files changed, 10 insertions(+), 2 deletions(-)"
			for _, part := range strings.Split(line, ",") {
				w := strings.Fields(part)
				if len(w) < 2 {
					continue
				}
				n, _ := strconv.Atoi(w[0])
				switch {
				case strings.HasPrefix(w[1], "file"):
					e.Files = n
				case strings.HasPrefix(w[1], "insertion"):
					e.Added = n
				case strings.HasPrefix(w[1], "deletion"):
					e.Deleted = n
				}
			}
		}
	}
	return entries, nil
}

// Diff returns the unified diff between two snapshots; with from == "" it is
// the diff of to against its parent (or the empty tree for the root commit).
func (s *Shadow) Diff(from, to string) (string, error) {
	var out []byte
	var err error
	if from == "" {
		out, err = s.git("show", "--format=", "-M", to)
	} else {
		out, err = s.git("diff", "-M", from, to)
	}
	return string(out), err
}
