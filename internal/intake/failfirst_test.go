package intake

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
)

// fakeBase answers each command with an exit code from a map; a command not in the map exits 1.
type fakeBase struct {
	mu     sync.Mutex
	exits  map[string]int
	ran    []string
	opened int
	closed int
}

func (f *fakeBase) open(context.Context) (BaseRunner, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened++
	return f, nil
}

func (f *fakeBase) Run(_ context.Context, command string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ran = append(f.ran, command)
	if code, ok := f.exits[command]; ok {
		return code, nil
	}
	return 1, nil
}

func (f *fakeBase) Close() error { f.mu.Lock(); f.closed++; f.mu.Unlock(); return nil }

// withBase wires a fake base runner into the rig's handler.
func (r *rig) withBase(exits map[string]int) *fakeBase {
	f := &fakeBase{exits: exits}
	r.h.d.OpenBase = f.open
	return f
}

func TestDoneWhenThatPassesOnTheBaseSendsTheDraftBack(t *testing.T) {
	passing := draftOf(groupWithCheck(1, 1, tgSpec{}, "go vet ./..."))
	failing := draftOf(groupWithCheck(1, 1, tgSpec{}, "go test ./pkg1_1/..."))
	r := newRig(t, passing, failing)
	base := r.withBase(map[string]int{"go vet ./...": 0})

	if _, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	if len(r.sess.texts) != 2 {
		t.Fatalf("sends: %d", len(r.sess.texts))
	}
	wants := []string{"fail_first", "epic-1/tg-1.1/tsk-one.md", "go vet ./...", "fails until the change is made"}
	for _, want := range wants {
		if !strings.Contains(r.sess.texts[1], want) {
			t.Fatalf("finding text lacks %q: %q", want, r.sess.texts[1])
		}
	}
	if base.opened != 1 || base.closed != 1 || len(base.ran) != 2 {
		t.Fatalf("opened %d closed %d ran %v", base.opened, base.closed, base.ran)
	}
	if _, err := os.Stat(r.backlog("epic-1/tg-1.1/TG.md")); err != nil {
		t.Fatalf("the accepted draft was not written: %v", err)
	}
}

func TestDoneWhenThatFailsOnTheBaseIsAccepted(t *testing.T) {
	r := newRig(t, draftOf(groupWithCheck(1, 1, tgSpec{}, "go test ./pkg1_1/...")))
	base := r.withBase(nil)

	if _, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	if len(r.sess.texts) != 1 || len(base.ran) != 1 {
		t.Fatalf("sends %d ran %v", len(r.sess.texts), base.ran)
	}
}

func TestThirdDraftThatPassesOnTheBaseBlocksThePlan(t *testing.T) {
	d := draftOf(groupWithCheck(1, 1, tgSpec{}, "go vet ./..."))
	r := newRig(t, d, d, d)
	base := r.withBase(map[string]int{"go vet ./...": 0})

	_, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g"})
	var blocked PlanBlockedError
	if !errors.As(err, &blocked) || len(blocked.Findings) != 1 || blocked.Findings[0].Rule != RuleFailFirst {
		t.Fatalf("err: %v", err)
	}
	if len(base.ran) != 1 {
		t.Fatalf("the same command ran %d times; want 1: %v", len(base.ran), base.ran)
	}
	if _, err := os.Stat(r.backlog("epic-1")); !os.IsNotExist(err) {
		t.Fatal("a blocked plan wrote an epic")
	}
}

func TestBaseRunsOnlyAfterLintPasses(t *testing.T) {
	r := newRig(t, badDraft(), twoGroupDraft())
	base := r.withBase(nil)

	if _, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	if base.opened != 1 || len(base.ran) != 1 {
		t.Fatalf("opened %d ran %v; the failing draft must not reach the base check", base.opened, base.ran)
	}
}

func TestBaseThatCannotOpenEndsThePlanWithZeroFiles(t *testing.T) {
	r := newRig(t, twoGroupDraft())
	r.h.d.OpenBase = func(context.Context) (BaseRunner, error) { return nil, errors.New("no sandbox") }

	_, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g"})
	if err == nil || !strings.Contains(err.Error(), "no sandbox") {
		t.Fatalf("err: %v", err)
	}
	if got := r.entries(); len(got) != 0 {
		t.Fatalf("entries: %v", got)
	}
}
