package commit

import (
	"strings"
	"testing"
)

// FindPR reports none, OpenPR makes the label and a ready pull request, and a later FindPR sees it.
func TestForgeOpenFindUpdate(t *testing.T) {
	isolate(t)
	fg := newFakeGH(t)
	f := NewGH(t.TempDir())
	ctx := t.Context()
	if _, ok, err := f.FindPR(ctx, "komodo/ep-1"); err != nil || ok {
		t.Fatalf("find before open = %v, %v", ok, err)
	}
	pr, err := f.OpenPR(ctx, "main", "komodo/ep-1", "ep-1: 2 task groups", "body text\n- a", "komodo")
	if err != nil || pr.URL != "https://github.com/acme/shop/pull/41" {
		t.Fatalf("open = %+v, %v", pr, err)
	}
	calls := strings.Join(fg.calls(), "\n")
	for _, want := range []string{"label create komodo --force", "pr create", "--base main", "--head komodo/ep-1", "--title ep-1: 2 task groups", "--label komodo"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("calls lack %q:\n%s", want, calls)
		}
	}
	if strings.Contains(calls, "--draft") {
		t.Fatal("opened as a draft")
	}
	if got := fg.file("create-body.txt"); got != "body text\n- a" {
		t.Fatalf("body = %q", got)
	}
	found, ok, err := f.FindPR(ctx, "komodo/ep-1")
	if err != nil || !ok || found.URL != pr.URL || found.HeadSHA != "abc123" {
		t.Fatalf("find = %+v %v %v", found, ok, err)
	}
	if err := f.UpdatePR(ctx, pr.URL, "ep-1: 3 task groups", "new body"); err != nil {
		t.Fatal(err)
	}
	if got := fg.file("edit-body.txt"); got != "new body" {
		t.Fatalf("edit body = %q", got)
	}
	if !strings.Contains(strings.Join(fg.calls(), "\n"), "pr edit "+pr.URL+" --title ep-1: 3 task groups") {
		t.Fatalf("calls = %v", fg.calls())
	}
}

// A gh failure comes back as an error that names the failing command.
func TestForgeError(t *testing.T) {
	isolate(t)
	newFakeGH(t)
	f := NewGH(t.TempDir())
	f.Bin = "gh-missing-program"
	if _, _, err := f.FindPR(t.Context(), "komodo/ep-1"); err == nil {
		t.Fatal("want an error")
	}
}
