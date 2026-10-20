package commit

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// treeChecker copies groupChecker's landed-tree rule from cmd/komodo/wire_recheck.go.
type treeChecker struct{ group string }

func (c treeChecker) Recheck(_ context.Context, _, landing string, landed bool) (CheckResult, error) {
	if !landed {
		return CheckResult{Pass: true}, nil
	}
	g, _ := exec.Command("git", "-C", c.group, "rev-parse", "HEAD^{tree}").Output()
	e, _ := exec.Command("git", "-C", landing, "rev-parse", "HEAD^{tree}").Output()
	if strings.TrimSpace(string(g)) != strings.TrimSpace(string(e)) {
		return CheckResult{}, errors.New("the group's checkout differs from the epic tip")
	}
	return CheckResult{Pass: true}, nil
}

// tg-1.1 lands, its cleanup fails so the group blocks, tg-1.2 lands, then tg-1.1 is approved again.
func TestRepublishAfterAnotherLandingSkipsTheStaleRecheck(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	g1 := w.newGroup("tg-1.1")
	w.work(g1, "a.txt", "a")
	h.Check = treeChecker{group: g1.wt}
	if _, err := h.Publish(w.ctx, w.approval(g1, "tg-1.1", "tg-1.2")); err != nil {
		t.Fatalf("publish tg-1.1: %v", err)
	}
	g2 := w.newGroup("tg-1.2")
	w.work(g2, "b.txt", "b")
	h.Check = treeChecker{group: g2.wt}
	if _, err := h.Publish(w.ctx, w.approval(g2, "tg-1.1", "tg-1.2")); err != nil {
		t.Fatalf("publish tg-1.2: %v", err)
	}
	h.Check = treeChecker{group: g1.wt}
	res, err := h.Publish(w.ctx, w.approval(g1, "tg-1.1", "tg-1.2"))
	t.Logf("republish tg-1.1: res=%+v err=%v", res, err)
	if err != nil {
		t.Fatalf("a landed group approved again fails: %v", err)
	}
}
