package task

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepo is a temporary repository with one linked worktree, as the lease records it.
type gitRepo struct {
	Main  string
	Lease LeaseRef
}

// newGitRepo makes a repository with one commit and a linked worktree on its own branch.
func newGitRepo(t *testing.T) gitRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := gitRepo{Main: filepath.Join(base, "main")}
	if err := os.MkdirAll(r.Main, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, r.Main, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(r.Main, "a.go"), "package a\n")
	writeFile(t, filepath.Join(r.Main, ".gitignore"), "ignored.log\n")
	git(t, r.Main, "add", "-A")
	git(t, r.Main, "commit", "-q", "-m", "first")
	wt := filepath.Join(base, "wt")
	git(t, r.Main, "worktree", "add", "-q", "-b", "komodo/ep-1-tg-1", wt)
	r.Lease = LeaseRef{Worktree: wt, GitDir: filepath.Join(r.Main, ".git", "worktrees", "wt")}
	return r
}

func TestHostGitRefusesAChangedPointer(t *testing.T) {
	r := newGitRepo(t)
	h := newHostGit(t, r)
	if _, err := h.Run(context.Background(), "status", "--porcelain"); err != nil {
		t.Fatalf("status with the pointer unchanged: %v", err)
	}
	// A confined command rewrites the pointer to a folder whose configuration runs a script.
	marker := filepath.Join(t.TempDir(), "ran")
	evil := filepath.Join(t.TempDir(), "evil.git")
	git(t, filepath.Dir(evil), "init", "-q", "--bare", evil)
	script := filepath.Join(t.TempDir(), "mon.sh")
	writeFile(t, script, "#!/bin/sh\ntouch '"+marker+"'\n")
	git(t, evil, "config", "core.fsmonitor", script)
	writeFile(t, filepath.Join(r.Lease.Worktree, ".git"), "gitdir: "+evil+"\n")
	for _, call := range []func() error{
		func() error { _, err := h.Run(context.Background(), "status"); return err },
		func() error { _, err := h.ReadOnly(context.Background(), "log", nil); return err },
		func() error { _, err := h.Diff(context.Background(), nil); return err },
		func() error { _, err := h.TreeHash(context.Background()); return err },
	} {
		if err := call(); !errors.Is(err, ErrPointerChanged) {
			t.Fatalf("call after the pointer changed returned %v, want ErrPointerChanged", err)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the planted script ran")
	}
	// Putting the text back restores the calls: the check compares exact text.
	writeFile(t, filepath.Join(r.Lease.Worktree, ".git"), string(h.ptr.text))
	if _, err := h.Run(context.Background(), "status"); err != nil {
		t.Fatalf("status after restoring the pointer: %v", err)
	}
}

func TestHostGitRunsNoPlantedScript(t *testing.T) {
	r := newGitRepo(t)
	marker := filepath.Join(t.TempDir(), "ran")
	script := filepath.Join(t.TempDir(), "mon.sh")
	writeFile(t, script, "#!/bin/sh\ntouch '"+marker+"'\n")
	hooks := filepath.Join(t.TempDir(), "hooks")
	writeFile(t, filepath.Join(hooks, "post-checkout"), "#!/bin/sh\ntouch '"+marker+"'\n")
	git(t, r.Main, "config", "core.fsmonitor", script)
	git(t, r.Main, "config", "core.hooksPath", hooks)
	// Control: git with the repository's own configuration runs the script.
	git(t, r.Lease.Worktree, "status")
	if _, err := os.Stat(marker); err != nil {
		t.Skip("this git does not run the fsmonitor script on status, so the proof cannot be made here")
	}
	_ = os.Remove(marker)
	h := newHostGit(t, r)
	ctx := context.Background()
	for _, f := range []func() error{
		func() error { _, err := h.ReadOnly(ctx, "status", nil); return err },
		func() error { _, err := h.Diff(ctx, nil); return err },
		func() error { _, err := h.TreeHash(ctx); return err },
	} {
		if err := f(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a host git call ran the planted fsmonitor or hook script")
	}
}

func TestHostGitDiffAndTreeHash(t *testing.T) {
	r := newGitRepo(t)
	h := newHostGit(t, r)
	ctx := context.Background()
	h0, err := h.TreeHash(ctx)
	if err != nil || len(h0) < 40 {
		t.Fatalf("tree hash %q: %v", h0, err)
	}
	writeFile(t, filepath.Join(r.Lease.Worktree, "a.go"), "package a\n\nfunc A() {}\n")
	writeFile(t, filepath.Join(r.Lease.Worktree, "new.go"), "package a\n")
	writeFile(t, filepath.Join(r.Lease.Worktree, "ignored.log"), "noise\n")
	h1, _ := h.TreeHash(ctx)
	if h1 == h0 {
		t.Fatal("tree hash did not change after an edit")
	}
	if h2, _ := h.TreeHash(ctx); h2 != h1 {
		t.Fatal("tree hash is not stable")
	}
	d, err := h.Diff(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"+func A() {}", "b/new.go", "diff --git a/a.go"} {
		if !strings.Contains(d, want) {
			t.Errorf("diff lacks %q:\n%s", want, d)
		}
	}
	if strings.Contains(d, "ignored.log") {
		t.Error("diff shows an ignored file")
	}
	only, _ := h.Diff(ctx, []string{"new.go"})
	if strings.Contains(only, "a.go") || !strings.Contains(only, "new.go") {
		t.Errorf("diff for one path:\n%s", only)
	}
	if st := git(t, r.Lease.Worktree, "status", "--porcelain"); !strings.Contains(st, "?? new.go") {
		t.Errorf("the real index was changed by the diff:\n%s", st)
	}
}

func TestHostGitReadOnlyRunsOnlyReads(t *testing.T) {
	r := newGitRepo(t)
	h := newHostGit(t, r)
	if out, err := h.ReadOnly(context.Background(), "log", []string{"--oneline"}); err != nil ||
		!strings.Contains(out, "first") {
		t.Fatalf("log: %q %v", out, err)
	}
	for _, c := range []string{"commit", "push", "reset", "checkout", "config", "add"} {
		if _, err := h.ReadOnly(context.Background(), c, nil); err == nil {
			t.Errorf("git %s ran through ReadOnly", c)
		}
	}
}

// git runs a plain git command in dir for test setup, with no user or system configuration.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(
		"git",
		append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...,
	)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dir,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// writeFile writes a test file, making its folder.
func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

// newHostGit builds the runner for a repository's worktree.
func newHostGit(t *testing.T, r gitRepo) *HostGit {
	t.Helper()
	ptr, err := RecordPointer(r.Lease.Worktree)
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHostGit(r.Lease, "", ptr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}
