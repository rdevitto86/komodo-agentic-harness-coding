package core

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TreeOf counts untracked files, skips ignored ones and leaves the real index alone.
func TestTreeOfCountsUntrackedSkipsIgnored(t *testing.T) {
	w := newWorld(t)
	g := w.newGroup("tg-1.1")
	_, base, err := w.git.Head(w.ctx, g.wt)
	if err != nil {
		t.Fatal(err)
	}
	write(t, g.wt, ".gitignore", "*.log\n")
	t1, err := w.git.TreeOf(w.ctx, g.wt)
	if err != nil {
		t.Fatal(err)
	}
	write(t, g.wt, "debug.log", "noise")
	t2, _ := w.git.TreeOf(w.ctx, g.wt)
	if t1 != t2 {
		t.Fatalf("ignored file changed the tree: %s vs %s", t1, t2)
	}
	write(t, g.wt, "new.txt", "x")
	t3, _ := w.git.TreeOf(w.ctx, g.wt)
	if t3 == t2 || t3 == base {
		t.Fatalf("untracked file not counted: %s", t3)
	}
	if st := git(t, g.wt, "diff", "--cached", "--name-only"); st != "" {
		t.Fatalf("TreeOf staged files in the real index: %q", st)
	}
}

// Commit records the content, the tree matches TreeOf, and a repeat with no change returns the same commit.
func TestCommitRecordsTreeAndIsIdempotent(t *testing.T) {
	w := newWorld(t)
	g := w.newGroup("tg-1.1")
	w.work(g, "a.txt", "a")
	want, _ := w.git.TreeOf(w.ctx, g.wt)
	c1, err := w.git.Commit(w.ctx, g.wt, "tg-1.1: first")
	if err != nil {
		t.Fatal(err)
	}
	head, tree, _ := w.git.Head(w.ctx, g.wt)
	if head != c1 || tree != want {
		t.Fatalf("head %s tree %s, want %s %s", head, tree, c1, want)
	}
	c2, err := w.git.Commit(w.ctx, g.wt, "tg-1.1: again")
	if err != nil || c2 != c1 {
		t.Fatalf("repeat commit = %s, %v; want %s", c2, err, c1)
	}
	info, err := w.git.Info(w.ctx, g.wt, "HEAD")
	if err != nil || info.Subject != "tg-1.1: first" || info.Tree != want || info.Parent == "" {
		t.Fatalf("info = %+v, %v", info, err)
	}
}

// A message that ends in Name: value lines is refused before git runs.
func TestCommitRefusesTrailer(t *testing.T) {
	w := newWorld(t)
	g := w.newGroup("tg-1.1")
	w.work(g, "a.txt", "a")
	n := len(w.traced)
	_, err := w.git.Commit(w.ctx, g.wt, "tg-1.1: first\n\nCo-Authored-By: Someone <a@b.c>")
	if !errors.Is(err, ErrTrailer) {
		t.Fatalf("err = %v, want ErrTrailer", err)
	}
	for _, a := range w.traced[n:] {
		for _, s := range a {
			if s == "commit" {
				t.Fatalf("git commit ran: %v", a)
			}
		}
	}
}

// Commit on a branch outside komodo/ is refused.
func TestCommitRefusesCriticalBranch(t *testing.T) {
	w := newWorld(t)
	write(t, w.clone, "x.txt", "x")
	_, err := w.git.Commit(w.ctx, w.clone, "msg")
	if !errors.Is(err, ErrCriticalRef) {
		t.Fatalf("err = %v, want ErrCriticalRef", err)
	}
	if strings.Contains(git(t, w.clone, "log", "--format=%s", "-1"), "msg") {
		t.Fatal("main moved")
	}
}

// Push of a reference outside komodo/ returns ErrCriticalRef and runs no git.
func TestPushRefusesCriticalRef(t *testing.T) {
	w := newWorld(t)
	for _, b := range []string{"main", "refs/heads/main", "refs/tags/v1", "komodo", "refs/komodo/x", "komodo/../main", ""} {
		n := len(w.traced)
		err := w.git.Push(w.ctx, w.clone, "origin", b)
		if !errors.Is(err, ErrCriticalRef) {
			t.Fatalf("push %q err = %v, want ErrCriticalRef", b, err)
		}
		if len(w.traced) != n {
			t.Fatalf("push %q ran git: %v", b, w.traced[n:])
		}
	}
}

// Push sends the branch to its own name and RemoteTip reads it back.
func TestPushAndRemoteTip(t *testing.T) {
	w := newWorld(t)
	g := w.newGroup("tg-1.1")
	if _, ok, err := w.git.RemoteTip(w.ctx, g.wt, "origin", g.branch); err != nil || ok {
		t.Fatalf("tip before push = %v, %v", ok, err)
	}
	w.work(g, "a.txt", "a")
	c, _ := w.git.Commit(w.ctx, g.wt, "tg-1.1: a")
	if err := w.git.Push(w.ctx, g.wt, "origin", g.branch); err != nil {
		t.Fatal(err)
	}
	tip, ok, err := w.git.RemoteTip(w.ctx, g.wt, "origin", g.branch)
	if err != nil || !ok || tip != c {
		t.Fatalf("tip = %s %v %v, want %s", tip, ok, err, c)
	}
}

// A remote that holds extra commits rejects the push, and the remote stays as it was.
func TestPushRejectedLeavesRemote(t *testing.T) {
	w := newWorld(t)
	g := w.newGroup("tg-1.1")
	w.work(g, "a.txt", "a")
	if _, err := w.git.Commit(w.ctx, g.wt, "tg-1.1: a"); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(w.root, "other")
	git(t, w.root, "clone", w.remote, other)
	git(t, other, "checkout", "-b", g.branch)
	write(t, other, "b.txt", "b")
	git(t, other, "add", "-A")
	git(t, other, "commit", "-m", "extra")
	git(t, other, "push", "origin", g.branch)
	before := w.remoteTip(g.branch)
	err := w.git.Push(w.ctx, g.wt, "origin", g.branch)
	if !errors.Is(err, ErrPushRejected) {
		t.Fatalf("err = %v, want ErrPushRejected", err)
	}
	if after := w.remoteTip(g.branch); after != before {
		t.Fatalf("remote moved %s -> %s", before, after)
	}
}

// Across a whole workflow no push holds a force flag, a plus refspec or an empty-source refspec; only a fetch into refs/remotes/ may carry a plus.
func TestNoForceInAnyArgument(t *testing.T) {
	w := newWorld(t)
	g := w.newGroup("tg-1.1")
	w.work(g, "a.txt", "a")
	_, _ = w.git.Commit(w.ctx, g.wt, "tg-1.1: a")
	_ = w.git.FetchBranch(w.ctx, g.wt, "origin", "main")
	_, _ = w.git.Merge(w.ctx, g.wt, "origin/komodo/ep-1", "tg-1.1: merge ep-1", false)
	_ = w.git.Push(w.ctx, g.wt, "origin", g.branch)
	_, _, _ = w.git.RemoteTip(w.ctx, g.wt, "origin", g.branch)
	_, _ = w.git.Landed(w.ctx, g.wt, "origin/main", "HEAD")
	_, _ = w.git.DiffStat(w.ctx, g.wt, "origin/main", "HEAD")
	_ = w.git.RemoveWorktree(w.ctx, w.clone, g.wt)
	head := git(t, w.clone, "rev-parse", g.branch)
	_ = w.git.DeleteBranch(w.ctx, w.clone, g.branch, head, "origin/main")
	_ = w.git.DiscardBranch(w.ctx, w.clone, g.branch, head)
	if len(w.traced) < 12 {
		t.Fatalf("only %d command lines traced", len(w.traced))
	}
	for _, a := range w.traced {
		for i, s := range a {
			plusOK := subcommand(a) == "fetch" && strings.HasPrefix(s, "+refs/heads/") && strings.Contains(s, ":refs/remotes/")
			bad := s == "-f" || strings.HasPrefix(s, "--force") || (strings.HasPrefix(s, "+") && !plusOK) || strings.HasPrefix(s, ":") || s == "--delete" || s == "-D"
			if bad {
				t.Fatalf("argument %d of %v forbids force or delete", i, a)
			}
		}
	}
}

// The wrapper itself refuses an argument list that would force or delete.
func TestRunRefusesForceArguments(t *testing.T) {
	w := newWorld(t)
	for _, a := range [][]string{{"push", "--force"}, {"push", "-f"}, {"push", "--force-with-lease"}, {"push", "origin", "+refs/heads/x:refs/heads/x"}, {"push", "origin", ":refs/heads/x"}} {
		if err := checkArgs(a); !errors.Is(err, ErrForce) {
			t.Fatalf("%v: err = %v, want ErrForce", a, err)
		}
	}
	if err := checkArgs([]string{"push", "origin", "refs/heads/komodo/x:refs/heads/komodo/x"}); err != nil {
		t.Fatal(err)
	}
	_ = w
}

// A pointer file that names another folder is ignored: the commit lands in the group's own git folder.
func TestPointerFileIgnored(t *testing.T) {
	w := newWorld(t)
	g := w.newGroup("tg-1.1")
	bait := filepath.Join(w.root, "bait.git")
	git(t, w.root, "init", "-b", "main", bait)
	if err := os.WriteFile(filepath.Join(g.wt, ".git"), []byte("gitdir: "+filepath.Join(bait, ".git")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.work(g, "a.txt", "a")
	c, err := w.git.Commit(w.ctx, g.wt, "tg-1.1: a")
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, w.clone, "rev-parse", g.branch); got != c {
		t.Fatalf("group branch = %s, want the new commit %s", got, c)
	}
	if out, err := execOut(bait, "rev-parse", "HEAD"); err == nil {
		t.Fatalf("bait repository gained a commit: %s", out)
	}
}

// A conflicting merge returns ErrConflict, aborts, and leaves the branch where it was.
func TestMergeConflictAborts(t *testing.T) {
	w := newWorld(t)
	g := w.newGroup("tg-1.1")
	w.work(g, "README.md", "group\n")
	before, _ := w.git.Commit(w.ctx, g.wt, "tg-1.1: edit")
	git(t, w.clone, "checkout", "komodo/ep-1")
	write(t, w.clone, "README.md", "epic\n")
	git(t, w.clone, "commit", "-am", "epic edit")
	git(t, w.clone, "checkout", "main")
	_, err := w.git.Merge(w.ctx, g.wt, "komodo/ep-1", "tg-1.1: merge ep-1", false)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	head, _, _ := w.git.Head(w.ctx, g.wt)
	if head != before {
		t.Fatalf("branch moved to %s", head)
	}
	if st := git(t, g.wt, "status", "--porcelain"); st != "" {
		t.Fatalf("merge left changes: %q", st)
	}
}

// Landed reads group landings back from first-parent merges, newest first, with sections and files.
func TestLandedAndDiffStat(t *testing.T) {
	w := newWorld(t)
	g := w.newGroup("tg-1.1")
	w.work(g, "a.txt", "a")
	_, _ = w.git.Commit(w.ctx, g.wt, "tg-1.1: a")
	land := filepath.Join(w.root, "land")
	if err := w.git.AddWorktree(w.ctx, w.clone, land, "komodo/ep-1", "origin/komodo/ep-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.git.Merge(w.ctx, land, g.branch, "tg-1.1: Goal one\n\n## Summary\n1 file", true); err != nil {
		t.Fatal(err)
	}
	g2 := w.newGroup("tg-1.2")
	w.work(g2, "b.txt", "b")
	_, _ = w.git.Commit(w.ctx, g2.wt, "tg-1.2: b")
	if _, err := w.git.Merge(w.ctx, land, g2.branch, "tg-1.2: Goal two\n\n## Summary\n2 file", true); err != nil {
		t.Fatal(err)
	}
	ls, err := w.git.Landed(w.ctx, land, "origin/main", "HEAD")
	if err != nil || len(ls) != 2 {
		t.Fatalf("landed = %+v, %v", ls, err)
	}
	if ls[0].Label != "tg-1.2" || ls[0].Goal != "Goal two" || ls[0].Section != "## Summary\n2 file" {
		t.Fatalf("newest = %+v", ls[0])
	}
	if ls[1].Label != "tg-1.1" {
		t.Fatalf("oldest = %+v", ls[1])
	}
	ds, err := w.git.DiffStat(w.ctx, land, "origin/main", "HEAD")
	if err != nil || !strings.Contains(ds, "2 files changed") {
		t.Fatalf("diffstat = %q, %v", ds, err)
	}
}

// RemoveWorktree refuses a worktree with files the tip lacks and removes a clean one.
func TestRemoveWorktree(t *testing.T) {
	w := newWorld(t)
	g := w.newGroup("tg-1.1")
	w.work(g, "a.txt", "a")
	if err := w.git.RemoveWorktree(w.ctx, w.clone, g.wt); !errors.Is(err, ErrDirty) {
		t.Fatalf("err = %v, want ErrDirty", err)
	}
	if _, err := os.Stat(filepath.Join(g.wt, "a.txt")); err != nil {
		t.Fatal("dirty worktree was touched")
	}
	_, _ = w.git.Commit(w.ctx, g.wt, "tg-1.1: a")
	if err := w.git.RemoveWorktree(w.ctx, w.clone, g.wt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(g.wt); !os.IsNotExist(err) {
		t.Fatal("worktree still there")
	}
}

// DeleteBranch deletes only when the tip matches and the history holds it; DiscardBranch needs only the tip.
func TestDeleteAndDiscardBranch(t *testing.T) {
	w := newWorld(t)
	git(t, w.clone, "branch", "komodo/x", "main")
	tip := git(t, w.clone, "rev-parse", "komodo/x")
	if err := w.git.DeleteBranch(w.ctx, w.clone, "komodo/x", strings.Repeat("0", 40), "main"); !errors.Is(err, ErrTipMoved) {
		t.Fatalf("err = %v, want ErrTipMoved", err)
	}
	if err := w.git.DeleteBranch(w.ctx, w.clone, "main", tip, "main"); !errors.Is(err, ErrCriticalRef) {
		t.Fatalf("err = %v, want ErrCriticalRef", err)
	}
	git(t, w.clone, "checkout", "-b", "komodo/y")
	write(t, w.clone, "y.txt", "y")
	git(t, w.clone, "add", "-A")
	git(t, w.clone, "commit", "-m", "y")
	git(t, w.clone, "checkout", "main")
	ytip := git(t, w.clone, "rev-parse", "komodo/y")
	if err := w.git.DeleteBranch(w.ctx, w.clone, "komodo/y", ytip, "main"); !errors.Is(err, ErrNotContained) {
		t.Fatalf("err = %v, want ErrNotContained", err)
	}
	if err := w.git.DeleteBranch(w.ctx, w.clone, "komodo/x", tip, "main"); err != nil {
		t.Fatal(err)
	}
	if err := w.git.DiscardBranch(w.ctx, w.clone, "komodo/y", ytip); err != nil {
		t.Fatal(err)
	}
	if ok, _ := w.git.BranchExists(w.ctx, w.clone, "komodo/y"); ok {
		t.Fatal("y still exists")
	}
}

// BranchExists sees local and remote-tracking branches; CreateBranch cuts without a checkout and refuses outside komodo/.
func TestBranchExistsAndCreate(t *testing.T) {
	w := newWorld(t)
	if ok, err := w.git.BranchExists(w.ctx, w.clone, "komodo/ep-1"); err != nil || !ok {
		t.Fatalf("ep-1 exists = %v, %v", ok, err)
	}
	if ok, _ := w.git.BranchExists(w.ctx, w.clone, "komodo/ep-2"); ok {
		t.Fatal("ep-2 should not exist")
	}
	if err := w.git.CreateBranch(w.ctx, w.clone, "komodo/ep-2", "origin/main"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := w.git.BranchExists(w.ctx, w.clone, "komodo/ep-2"); !ok {
		t.Fatal("ep-2 not created")
	}
	if err := w.git.CreateBranch(w.ctx, w.clone, "main2", "origin/main"); !errors.Is(err, ErrCriticalRef) {
		t.Fatalf("err = %v, want ErrCriticalRef", err)
	}
}

// Fetch of a missing branch returns ErrBranchMissing; DefaultBranch and UpstreamRemote read the configuration.
func TestFetchDefaultBranchUpstream(t *testing.T) {
	w := newWorld(t)
	if err := w.git.FetchBranch(w.ctx, w.clone, "origin", "main"); err != nil {
		t.Fatal(err)
	}
	if err := w.git.FetchBranch(w.ctx, w.clone, "origin", "nope"); !errors.Is(err, ErrBranchMissing) {
		t.Fatalf("err = %v, want ErrBranchMissing", err)
	}
	if b, err := w.git.DefaultBranch(w.ctx, w.clone, "origin"); err != nil || b != "main" {
		t.Fatalf("default = %q, %v", b, err)
	}
	if r, ok, err := w.git.UpstreamRemote(w.ctx, w.clone, "main"); err != nil || ok {
		t.Fatalf("upstream of untracked main = %q %v %v", r, ok, err)
	}
	git(t, w.clone, "branch", "--set-upstream-to=origin/main", "main")
	if r, ok, err := w.git.UpstreamRemote(w.ctx, w.clone, "main"); err != nil || !ok || r != "origin" {
		t.Fatalf("upstream = %q %v %v", r, ok, err)
	}
}

// AddWorktree is a no-op for a worktree that already exists on the branch.
func TestAddWorktreeIdempotent(t *testing.T) {
	w := newWorld(t)
	g := w.newGroup("tg-1.1")
	if err := w.git.AddWorktree(w.ctx, w.clone, g.wt, g.branch, "origin/komodo/ep-1"); err != nil {
		t.Fatalf("repeat add: %v", err)
	}
	if err := w.git.AddWorktree(w.ctx, w.clone, filepath.Join(w.root, "bad"), "main", "origin/main"); !errors.Is(err, ErrCriticalRef) {
		t.Fatalf("err = %v, want ErrCriticalRef", err)
	}
}
