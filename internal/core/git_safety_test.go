package core

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hookNames are the hooks a harness command could trigger.
var hookNames = []string{"pre-commit", "commit-msg", "post-commit", "pre-merge-commit", "post-merge", "pre-push", "reference-transaction", "post-checkout", "post-rewrite", "pre-auto-gc"}

// plantHooks writes a hook of every name into dir; each one appends to marker when it runs.
func plantHooks(t *testing.T, dir, marker string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, h := range hookNames {
		body := "#!/bin/sh\necho " + h + " >> '" + marker + "'\nexit 0\n"
		if err := os.WriteFile(filepath.Join(dir, h), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// An edited hook, in the default folder or in a folder the repository configuration names, never runs.
func TestHooksNeverRun(t *testing.T) {
	w := newWorld(t)
	g := w.newGroup("tg-1.1")
	w.work(g, "a.txt", "a")
	if _, err := w.git.Commit(w.ctx, g.wt, "tg-1.1: a"); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(w.root, "other")
	git(t, w.root, "clone", w.remote, other)
	git(t, other, "checkout", "komodo/ep-1")
	write(t, other, "e.txt", "e")
	git(t, other, "add", "-A")
	git(t, other, "commit", "-m", "epic moves")
	git(t, other, "push", "origin", "komodo/ep-1")
	marker := filepath.Join(w.root, "hook-ran.txt")
	plantHooks(t, filepath.Join(w.clone, ".git", "hooks"), marker)
	plantHooks(t, filepath.Join(w.clone, ".husky"), marker)
	git(t, w.clone, "config", "core.hooksPath", filepath.Join(w.clone, ".husky"))

	if err := w.git.FetchBranch(w.ctx, g.wt, "origin", "komodo/ep-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.git.Merge(w.ctx, g.wt, "refs/remotes/origin/komodo/ep-1", "tg-1.1: merge ep-1", true); err != nil {
		t.Fatal(err)
	}
	w.work(g, "b.txt", "b")
	if _, err := w.git.Commit(w.ctx, g.wt, "tg-1.1: b"); err != nil {
		t.Fatal(err)
	}
	if err := w.git.Push(w.ctx, g.wt, "origin", g.branch); err != nil {
		t.Fatal(err)
	}
	land := filepath.Join(w.root, "land")
	if err := w.git.AddWorktree(w.ctx, w.clone, land, "komodo/ep-2", "origin/main"); err != nil {
		t.Fatal(err)
	}
	if err := w.git.RemoveWorktree(w.ctx, w.clone, land); err != nil {
		t.Fatal(err)
	}
	tip := git(t, w.clone, "rev-parse", g.branch)
	if err := w.git.DiscardBranch(w.ctx, w.clone, "komodo/ep-2", git(t, w.clone, "rev-parse", "komodo/ep-2")); err != nil {
		t.Fatal(err)
	}
	if err := w.git.DeleteBranch(w.ctx, w.clone, g.branch, tip, "refs/heads/"+g.branch); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(marker); err == nil {
		t.Fatalf("hooks ran:\n%s", b)
	}
}

// Git receives only the variables it needs: the login variables go to a command that talks to the remote, and secrets go nowhere.
func TestGitEnvironmentScrubbed(t *testing.T) {
	w := newWorld(t)
	g := w.newGroup("tg-1.1")
	w.work(g, "a.txt", "a")
	dump := filepath.Join(w.root, "env-dump.txt")
	script := "#!/bin/sh\n{ echo \"== $*\"; env; } >> '" + dump + "'\nexec git \"$@\"\n"
	wrapper := filepath.Join(w.root, "gitwrap")
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	t.Setenv("GH_TOKEN", "gh-secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-secret")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
	w.git.Bin = wrapper
	if _, err := w.git.Commit(w.ctx, g.wt, "tg-1.1: a"); err != nil {
		t.Fatal(err)
	}
	if err := w.git.Push(w.ctx, g.wt, "origin", g.branch); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	var pushes, locals int
	for _, seg := range strings.Split(string(b), "== ")[1:] {
		head, _, _ := strings.Cut(seg, "\n")
		for _, secret := range []string{"AWS_SECRET_ACCESS_KEY=", "ANTHROPIC_API_KEY=", "aws-secret", "anthropic-secret"} {
			if strings.Contains(seg, secret) {
				t.Fatalf("%q leaked into git %s", secret, head)
			}
		}
		hasLogin := strings.Contains(seg, "SSH_AUTH_SOCK=") || strings.Contains(seg, "GH_TOKEN=")
		if strings.Contains(" "+head+" ", " push ") {
			pushes++
			if !strings.Contains(seg, "SSH_AUTH_SOCK=/tmp/agent.sock") || !strings.Contains(seg, "GH_TOKEN=gh-secret") {
				t.Fatalf("push lacks the login variables:\n%s", seg)
			}
		} else {
			locals++
			if hasLogin {
				t.Fatalf("a local command received the login variables: %s", head)
			}
		}
	}
	if pushes != 1 || locals == 0 {
		t.Fatalf("pushes = %d, local commands = %d", pushes, locals)
	}
}

// A fetch follows a remote branch whose history was rewritten.
func TestFetchFollowsRewrittenBranch(t *testing.T) {
	w := newWorld(t)
	other := filepath.Join(w.root, "other")
	git(t, w.root, "clone", w.remote, other)
	git(t, other, "checkout", "komodo/ep-1")
	write(t, other, "v1.txt", "v1")
	git(t, other, "add", "-A")
	git(t, other, "commit", "-m", "v1")
	git(t, other, "push", "origin", "komodo/ep-1")
	if err := w.git.FetchBranch(w.ctx, w.clone, "origin", "komodo/ep-1"); err != nil {
		t.Fatal(err)
	}
	git(t, other, "reset", "--hard", "main")
	write(t, other, "v2.txt", "v2")
	git(t, other, "add", "-A")
	git(t, other, "commit", "-m", "v2")
	git(t, other, "push", "--force", "origin", "komodo/ep-1")
	if err := w.git.FetchBranch(w.ctx, w.clone, "origin", "komodo/ep-1"); err != nil {
		t.Fatalf("fetch after a rewrite: %v", err)
	}
	if got, want := git(t, w.clone, "rev-parse", "refs/remotes/origin/komodo/ep-1"), w.remoteTip("komodo/ep-1"); got != want {
		t.Fatalf("tracking ref = %s, remote = %s", got, want)
	}
	if err := w.git.Fetch(w.ctx, w.clone, "origin"); err != nil {
		t.Fatalf("fetch all after a rewrite: %v", err)
	}
}

// Landed counts only two-parent merges whose body is a group section.
func TestLandedIgnoresOtherMerges(t *testing.T) {
	w := newWorld(t)
	land := filepath.Join(w.root, "land")
	if err := w.git.AddWorktree(w.ctx, w.clone, land, "komodo/ep-1", "origin/komodo/ep-1"); err != nil {
		t.Fatal(err)
	}
	g := w.newGroup("tg-1.1")
	w.work(g, "a.txt", "a")
	if _, err := w.git.Commit(w.ctx, g.wt, "tg-1.1: a"); err != nil {
		t.Fatal(err)
	}
	side := w.newGroup("tg-9.9")
	w.work(side, "s.txt", "s")
	if _, err := w.git.Commit(w.ctx, side.wt, "tg-9.9: s"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.git.Merge(w.ctx, land, g.branch, "tg-1.1: Goal\n\n"+SectionMarker+"\n1 file", true); err != nil {
		t.Fatal(err)
	}
	if _, err := w.git.Merge(w.ctx, land, side.branch, "ep-1: update from origin", true); err != nil {
		t.Fatal(err)
	}
	other := w.newGroup("tg-8.8")
	w.work(other, "o.txt", "o")
	if _, err := w.git.Commit(w.ctx, other.wt, "tg-8.8: o"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.git.Merge(w.ctx, land, other.branch, "tg-8.8: sneaky\n\nnot a section", true); err != nil {
		t.Fatal(err)
	}
	ls, err := w.git.Landed(w.ctx, land, "origin/main", "HEAD")
	if err != nil || len(ls) != 1 || ls[0].Label != "tg-1.1" {
		t.Fatalf("landed = %+v, %v", ls, err)
	}
}

// The wrapper's refusals answer Refused true; a failure does not.
func TestRefusalsCarryRefused(t *testing.T) {
	for _, err := range []error{ErrDirty, ErrTipMoved, ErrNotContained, ErrCriticalRef, ErrForce} {
		var r interface{ Refused() bool }
		if !errors.As(err, &r) || !r.Refused() {
			t.Errorf("%v is not a refusal", err)
		}
	}
	var r interface{ Refused() bool }
	if errors.As(ErrConflict, &r) {
		t.Error("a conflict is a failure, not a refusal")
	}
}

// Fetch never prunes, Holds reads only what Fetch brought in, and the worktree and branch helpers work.
func TestCleanupMethods(t *testing.T) {
	w := newWorld(t)
	other := filepath.Join(w.root, "other")
	git(t, w.root, "clone", w.remote, other)
	git(t, other, "checkout", "komodo/ep-1")
	write(t, other, "n.txt", "n")
	git(t, other, "add", "-A")
	git(t, other, "commit", "-m", "n")
	git(t, other, "push", "origin", "komodo/ep-1")
	newTip := git(t, other, "rev-parse", "HEAD")
	if _, ok, err := w.git.Holds(w.ctx, w.clone, "origin", "origin/komodo/ep-1", newTip); err != nil || ok {
		t.Fatalf("holds before fetch = %v, %v; the remote must be read through the tracking refs only", ok, err)
	}
	if err := w.git.Fetch(w.ctx, w.clone, "origin"); err != nil {
		t.Fatal(err)
	}
	if ref, ok, err := w.git.Holds(w.ctx, w.clone, "origin", "origin/komodo/ep-1", newTip); err != nil || !ok || ref != "origin/komodo/ep-1" {
		t.Fatalf("holds = %q %v %v", ref, ok, err)
	}
	if ref, ok, err := w.git.Holds(w.ctx, w.clone, "origin", "", newTip); err != nil || !ok || ref != "origin/komodo/ep-1" {
		t.Fatalf("holds any = %q %v %v", ref, ok, err)
	}
	if _, ok, _ := w.git.Holds(w.ctx, w.clone, "origin", "origin/main", newTip); ok {
		t.Fatal("main does not hold the epic's new commit")
	}
	git(t, other, "push", "origin", ":komodo/ep-1")
	if err := w.git.Fetch(w.ctx, w.clone, "origin"); err != nil {
		t.Fatal(err)
	}
	if _, err := execOut(w.clone, "rev-parse", "--verify", "refs/remotes/origin/komodo/ep-1"); err != nil {
		t.Fatal("Fetch pruned a tracking reference")
	}
	if tip, ok, err := w.git.BranchTip(w.ctx, w.clone, "komodo/ep-1"); err != nil || !ok || tip == "" {
		t.Fatalf("branch tip = %q %v %v", tip, ok, err)
	}
	if _, ok, _ := w.git.BranchTip(w.ctx, w.clone, "komodo/nope"); ok {
		t.Fatal("missing branch reported")
	}
	g := w.newGroup("tg-1.1")
	if p, ok, err := w.git.WorktreeOf(w.ctx, w.clone, g.branch); err != nil || !ok || realPath(p) != realPath(g.wt) {
		t.Fatalf("worktree of = %q %v %v", p, ok, err)
	}
	if _, ok, _ := w.git.WorktreeOf(w.ctx, w.clone, "komodo/ep-1"); ok {
		t.Fatal("a branch no worktree holds was found")
	}
	w.work(g, "dirty.txt", "x")
	if err := w.git.DiscardWorktree(w.ctx, w.clone, g.wt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(g.wt); !os.IsNotExist(err) {
		t.Fatal("dirty worktree not discarded")
	}
	keep := filepath.Join(w.root, "keep")
	write(t, keep, "precious.txt", "p")
	link := filepath.Join(w.root, "link")
	if err := os.Symlink(keep, link); err != nil {
		t.Fatal(err)
	}
	if err := w.git.DiscardWorktree(w.ctx, w.clone, link); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(keep, "precious.txt")); err != nil {
		t.Fatal("a link was followed")
	}
	if ok, err := w.git.IsAncestor(w.ctx, w.clone, "refs/remotes/origin/main", "main"); err != nil || !ok {
		t.Fatalf("ancestor = %v, %v", ok, err)
	}
}
