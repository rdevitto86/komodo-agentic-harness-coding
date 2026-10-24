package intake

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAmendmentClosingACycleThroughAStoredGroupFails(t *testing.T) {
	_, epics := stored(t, goodGroup(1, 1, tgSpec{deps: []string{"tg-1.2"}}), goodGroup(1, 2, tgSpec{}))
	draft := draftOf(goodGroup(1, 2, tgSpec{deps: []string{"tg-1.1"}}))
	fs := rulesOf(lintDraftBacklog(draft, epics, nil, nil), RuleGraph, SeverityFail)
	if len(fs) != 1 || !strings.Contains(fs[0].Message, "cycle") {
		t.Fatalf("findings: %+v", fs)
	}
}

func TestOverlapWithAStoredLaterGroupFails(t *testing.T) {
	_, epics := stored(t,
		goodGroup(1, 1, tgSpec{touches: "y/**", files: []string{"y/a.go"}}),
		goodGroup(1, 2, tgSpec{touches: "x/**", files: []string{"x/b.go"}}))
	draft := draftOf(goodGroup(1, 1, tgSpec{touches: "x/**", files: []string{"x/a.go"}}))
	fs := rulesOf(lintDraftBacklog(draft, epics, nil, nil), RuleGraph, SeverityFail)
	if len(fs) != 1 || !strings.Contains(fs[0].Message, "x/a.go") {
		t.Fatalf("findings: %+v", fs)
	}
}

func TestFailedUndoKeepsTheOldFolderForRestore(t *testing.T) {
	old := goodGroup(1, 1, tgSpec{})
	r := newRig(t, draftOf(goodGroup(1, 1, tgSpec{goal: "Changed"})))
	writeBacklog(t, r.root, old)
	calls := 0
	r.h.rename = func(a, b string) error {
		calls++
		if calls >= 2 {
			return errors.New("disk gone")
		}
		return os.Rename(a, b)
	}
	_, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g", Epic: 1})
	if err == nil || !strings.Contains(err.Error(), "disk gone") {
		t.Fatalf("err: %v", err)
	}
	var aside string
	for _, n := range r.entries() {
		if strings.HasPrefix(n, ".plan-old-") {
			aside = n
		}
	}
	if aside == "" {
		t.Fatalf("the old folder was deleted: %v", r.entries())
	}
	if b, err := os.ReadFile(r.backlog(aside + "/epic-1/tg-1.1/TG.md")); err != nil || string(b) != old[0].Content {
		t.Fatalf("old content lost: %v", err)
	}
	if err := restoreOrphans(r.root); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(r.backlog("epic-1/tg-1.1/TG.md")); string(b) != old[0].Content {
		t.Fatal("restore did not bring the old group back")
	}
}

func TestTaskFilesDifferingOnlyByCaseFail(t *testing.T) {
	files := goodGroup(1, 1, tgSpec{})
	files = append(files, DraftFile{Path: "epic-1/tg-1.1/tsk-ONE.md", Content: files[1].Content})
	fs := rulesOf(lintDraftBacklog(draftOf(files), nil, nil, nil), RuleCaseCollision, SeverityFail)
	if len(fs) != 1 || !strings.Contains(fs[0].Message, "tsk-ONE.md") {
		t.Fatalf("findings: %+v", fs)
	}
}

func TestLeadingZerosInPathNumbersFail(t *testing.T) {
	for _, p := range []string{"epic-01/tg-1.1/TG.md", "epic-1/tg-01.1/TG.md", "epic-1/tg-1.01/TG.md"} {
		files := []DraftFile{{Path: p, Content: "files: a\n"}}
		fs := rulesOf(lintDraftBacklog(draftOf(files), nil, nil, nil), RuleGrammar, SeverityFail)
		if len(fs) == 0 || fs[0].Path != p {
			t.Errorf("%s: %+v", p, fs)
		}
	}
}

func TestZeroPaddedEpicCannotBypassTheLock(t *testing.T) {
	r := newRig(t, nil, nil, nil)
	old := goodGroup(1, 1, tgSpec{})
	writeBacklog(t, r.root, old)
	r.lock.runs[uid(101)] = "run-9"
	r.h.d.Locks = r.lock
	moved := goodGroup(1, 1, tgSpec{goal: "Changed"})
	for i := range moved {
		moved[i].Path = strings.Replace(moved[i].Path, "epic-1", "epic-01", 1)
	}
	r.sess.steps = []any{draftOf(moved), draftOf(moved), draftOf(moved)}
	_, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g", Epic: 1})
	if err == nil {
		t.Fatal("a zero-padded epic path must not pass")
	}
	if b, _ := os.ReadFile(
		filepath.Join(r.root, "docs", "backlog", "epic-1", "tg-1.1", "TG.md"),
	); string(b) != old[0].Content {
		t.Fatal("a locked group changed")
	}
}

func TestDraftGroupMayNotReuseAStoredID(t *testing.T) {
	_, epics := stored(t, goodGroup(1, 1, tgSpec{}))
	draft := draftOf(goodGroup(1, 2, tgSpec{id: uid(101)}))
	fs := rulesOf(lintDraftBacklog(draft, epics, nil, nil), RuleGrammar, SeverityFail)
	if len(fs) != 1 || !strings.Contains(fs[0].Message, "id") {
		t.Fatalf("findings: %+v", fs)
	}
}

func TestNewGroupsTakeTheNextNumbers(t *testing.T) {
	_, epics := stored(t, goodGroup(1, 1, tgSpec{}))
	cases := []struct {
		name  string
		draft PlanDraft
		old   []Epic
		ok    bool
	}{
		{"next number", draftOf(goodGroup(1, 2, tgSpec{})), epics, true},
		{"skipped number", draftOf(goodGroup(1, 3, tgSpec{})), epics, false},
		{"two new in order", draftOf(goodGroup(1, 2, tgSpec{}), goodGroup(1, 3, tgSpec{})), epics, true},
		{"amended group keeps its number", draftOf(goodGroup(1, 1, tgSpec{})), epics, true},
		{"new epic starts at 1", draftOf(goodGroup(2, 2, tgSpec{})), epics, false},
		{"new epic first group", draftOf(goodGroup(2, 1, tgSpec{})), epics, true},
	}
	for _, c := range cases {
		fs := rulesOf(lintDraftBacklog(c.draft, c.old, nil, nil), RuleGrammar, SeverityFail)
		if c.ok != (len(fs) == 0) {
			t.Errorf("%s: %+v", c.name, fs)
		}
	}
}

func TestPlanWritesStatusTodoOnly(t *testing.T) {
	for _, st := range []string{"done", "in_progress", "blocked"} {
		files := goodGroup(1, 1, tgSpec{status: st})
		fs := rulesOf(lintDraftBacklog(draftOf(files), nil, nil, nil), RuleGrammar, SeverityFail)
		if len(fs) != 1 || !strings.Contains(fs[0].Message, "todo") {
			t.Errorf("%s: %+v", st, fs)
		}
	}
}

// stored writes files as the stored task lists and returns the loaded epics.
func stored(t *testing.T, files ...[]DraftFile) (string, []Epic) {
	t.Helper()
	return loadFiles(t, cat(files...))
}
