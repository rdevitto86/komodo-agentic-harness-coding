package task

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitCLIAddsTheWorktreeOnceHashesTheTreeAndResetsTrackedEdits(t *testing.T) {
	r := newGitRepo(t)
	g := &GitCLI{Repo: r.Main, Base: "HEAD", Start: "main"}
	t.Cleanup(func() { _ = g.Close() })
	l := LeaseRef{
		Worktree: filepath.Join(filepath.Dir(r.Main), "wt9"),
		GitDir:   filepath.Join(r.Main, ".git", "worktrees", "wt9"),
	}
	ctx := context.Background()
	for range 2 {
		if err := g.AddWorktree(ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.TrimSpace(git(t, l.Worktree, "branch", "--show-current")); got != "" {
		t.Fatalf("worktree has branch %q checked out", got)
	}
	if head, tip := git(t, l.Worktree, "rev-parse", "HEAD"), git(t, r.Main, "rev-parse", "main"); head != tip {
		t.Fatalf("worktree HEAD %q, epic tip %q", head, tip)
	}
	if got := strings.TrimSpace(git(t, r.Main, "branch", "--list", "komodo/ep-1-tg-9")); got != "" {
		t.Fatalf("branch list %q, want none for the group", got)
	}
	if err := g.RecordPointer(l); err != nil {
		t.Fatal(err)
	}
	clean, err := g.TreeHash(ctx, l)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(l.Worktree, "a.go"), "package a\n\nvar X = 1\n")
	writeFile(t, filepath.Join(l.Worktree, "new.go"), "package a\n")
	edited, err := g.TreeHash(ctx, l)
	if err != nil || edited == clean {
		t.Fatalf("tree hash did not follow the edit: %v", err)
	}
	diff, err := g.Diff(ctx, l, nil)
	if err != nil || !strings.Contains(diff, "+var X = 1") {
		t.Fatalf("diff %q, %v", diff, err)
	}
	st, err := g.ReadOnly(ctx, l, "status", []string{"--porcelain"})
	if err != nil || len(trackedEdits(st)) != 1 {
		t.Fatalf("status %q, %v", st, err)
	}
	if err := g.ResetTracked(ctx, l); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(l.Worktree, "a.go")); string(b) != "package a\n" {
		t.Fatalf("a.go after reset: %q", b)
	}
	if _, err := os.Stat(filepath.Join(l.Worktree, "new.go")); err == nil {
		t.Fatal("the untracked file survived the reset")
	}
}

func TestBaseTreeReadsTheBaseCommitThroughTheHostRules(t *testing.T) {
	r := newGitRepo(t)
	g := &GitCLI{Repo: r.Main, Base: "HEAD"}
	t.Cleanup(func() { _ = g.Close() })
	h, err := g.host(r.Lease)
	if err != nil {
		t.Fatal(err)
	}
	bt := NewBaseTree(h, "HEAD")
	if got := bt.Paths(); len(got) != 2 || got[0] != ".gitignore" || got[1] != "a.go" {
		t.Fatalf("paths %v", got)
	}
	if b, err := bt.ReadFile("a.go"); err != nil || string(b) != "package a\n" {
		t.Fatalf("a.go: %q, %v", b, err)
	}
	if !bt.Exists("*.go") || bt.Exists("*.rs") || bt.Hash() == "" {
		t.Fatal("tree view answers wrongly")
	}
}
