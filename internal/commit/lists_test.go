package commit

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// SaveLists commits the epic's task lists onto the local epic branch as a new commit and pushes nothing; a repeat adds nothing.
func TestSaveListsCommitsLocallyAndPushesNothing(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	write(t, w.clone, "docs/backlog/epic-1/tg-1.1/TG.md", "# group\n")
	write(t, w.clone, "docs/backlog/epic-1/tg-1.1/tsk-a.md", "# task\n")
	write(t, w.clone, "docs/backlog/epic-2/tg-2.1/TG.md", "# other epic\n")
	before := w.remoteTip("komodo/ep-1")
	req := ListsRequest{Repo: "repo", Root: w.clone, CommonDir: filepath.Join(w.clone, ".git"), Epic: "ep-1"}
	if err := h.SaveLists(w.ctx, req); err != nil {
		t.Fatal(err)
	}
	tip := w.localTip()
	if w.remoteTip("komodo/ep-1") != before || w.pushes() != 0 {
		t.Fatal("the remote epic branch moved or a push ran")
	}
	if got := git(t, w.clone, "show", tip+":docs/backlog/epic-1/tg-1.1/TG.md"); got != "# group" {
		t.Fatalf("file = %q", got)
	}
	if out, err := execOut(w.clone, "cat-file", "-e", tip+":docs/backlog/epic-2/tg-2.1/TG.md"); err == nil {
		t.Fatalf("another epic's lists were committed: %s", out)
	}
	if subj := git(t, w.clone, "log", "-1", "--format=%s", tip); subj != "ep-1: task lists" {
		t.Fatalf("subject = %q", subj)
	}
	if err := h.SaveLists(w.ctx, req); err != nil {
		t.Fatal(err)
	}
	if w.localTip() != tip {
		t.Fatal("repeat added a commit")
	}
	if n := countLines(git(t, w.clone, "worktree", "list")); n != 1 {
		t.Fatalf("temporary worktree left behind: %d worktrees", n)
	}
}

// SaveLists with no lists folder for the epic fails and pushes nothing.
func TestSaveListsMissingFolder(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	before := w.remoteTip("komodo/ep-1")
	err := h.SaveLists(w.ctx, ListsRequest{
		Repo:      "repo",
		Root:      w.clone,
		CommonDir: filepath.Join(w.clone, ".git"),
		Epic:      "ep-1",
	})
	if err == nil || errors.Is(err, ErrCriticalRef) {
		t.Fatalf("err = %v", err)
	}
	if w.remoteTip("komodo/ep-1") != before {
		t.Fatal("remote moved")
	}
}

// countLines counts the non-empty lines of s.
func countLines(s string) int {
	n := 0
	for _, l := range strings.Split(s, "\n") {
		if l != "" {
			n++
		}
	}
	return n
}
