package core

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// isolate points git at a throwaway home so no test reads or writes the person's configuration.
func isolate(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	cfg := filepath.Join(home, "gitconfig")
	body := "[user]\n\tname = Test\n\temail = test@example.com\n[init]\n\tdefaultBranch = main\n[protocol \"file\"]\n\tallow = always\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR"} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
}

// git runs plain git in dir for test setup and checks, and returns trimmed output.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// execOut runs plain git in dir and returns its output and error.
func execOut(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// write creates a file under dir, with parents.
func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// world is a bare remote, a clone on main and the epic branch komodo/ep-1 on both.
type world struct {
	t      *testing.T
	ctx    context.Context
	root   string
	remote string
	clone  string
	git    *Git
	mu     sync.Mutex
	traced [][]string
}

// newWorld builds the repositories and a Git that records every command line.
func newWorld(t *testing.T) *world {
	t.Helper()
	isolate(t)
	root := t.TempDir()
	w := &world{t: t, ctx: context.Background(), root: root}
	w.remote = filepath.Join(root, "remote.git")
	w.clone = filepath.Join(root, "clone")
	git(t, root, "init", "--bare", "-b", "main", w.remote)
	git(t, root, "init", "-b", "main", w.clone)
	write(t, w.clone, "README.md", "hello\n")
	git(t, w.clone, "add", "-A")
	git(t, w.clone, "commit", "-m", "init")
	git(t, w.clone, "remote", "add", "origin", w.remote)
	git(t, w.clone, "push", "origin", "main")
	git(t, w.clone, "fetch", "origin")
	git(t, w.clone, "remote", "set-head", "origin", "main")
	git(t, w.clone, "branch", "komodo/ep-1", "main")
	git(t, w.clone, "push", "origin", "komodo/ep-1")
	git(t, w.clone, "fetch", "origin")
	w.git = NewGit(w.clone)
	w.git.Trace = func(a []string) {
		w.mu.Lock()
		w.traced = append(w.traced, append([]string(nil), a...))
		w.mu.Unlock()
	}
	return w
}

// group is one group's branch and checkout.
type group struct{ label, branch, wt string }

// newGroup cuts the group branch from the epic tip and adds its worktree.
func (w *world) newGroup(label string) group {
	w.t.Helper()
	g := group{label: label, branch: "komodo/ep-1-" + label, wt: filepath.Join(w.root, "wt", label)}
	if err := w.git.AddWorktree(w.ctx, w.clone, g.wt, g.branch, "origin/komodo/ep-1"); err != nil {
		w.t.Fatal(err)
	}
	return g
}

// work writes a file into the group's worktree.
func (w *world) work(g group, name, body string) { write(w.t, g.wt, name, body) }

// remoteTip returns the commit the bare remote holds for a branch.
func (w *world) remoteTip(branch string) string {
	return git(w.t, w.remote, "rev-parse", "refs/heads/"+branch)
}
