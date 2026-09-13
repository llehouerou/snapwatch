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

// Entry is one timeline row.
type Entry struct {
	SHA     string
	Time    time.Time
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

// Snapshot stages everything and commits if anything changed.
// Returns the new SHA, or "" when the tree was unchanged.
func (s *Shadow) Snapshot() (string, error) {
	if _, err := s.git("add", "-A"); err != nil {
		return "", err
	}
	_, err := s.git("diff", "--cached", "--quiet")
	var exit *exec.ExitError
	switch {
	case err == nil:
		return "", nil // nothing staged
	case !errors.As(err, &exit) || exit.ExitCode() != 1:
		return "", err
	}
	if _, err := s.git("commit", "-q", "--allow-empty-message", "-m", ""); err != nil {
		return "", err
	}
	out, err := s.git("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Log returns snapshots newest first: those newer than after, or up to n
// older than before (all when both are empty). The initial snapshot (root
// commit) is the baseline, not a change: skipped.
func (s *Shadow) Log(after, before string, n int) ([]Entry, error) {
	args := []string{"log", "--min-parents=1", "--format=%H%x00%ct%x00", "--shortstat"}
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
			entries = append(entries, Entry{SHA: f[0], Time: time.Unix(ts, 0)})
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
