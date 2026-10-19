package intake

import (
	"context"
	"os"
	"regexp"
	"testing"
)

// idLine reads the id value of a stored TG.md.
func idLine(t *testing.T, r *rig, rel string) string {
	t.Helper()
	b, err := os.ReadFile(r.backlog(rel))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^id: (\S+)$`).FindSubmatch(b)
	if m == nil {
		t.Fatalf("%s has no id line", rel)
	}
	return string(m[1])
}

func TestDraftWithoutIDsPassesLint(t *testing.T) {
	d := draftOf(goodGroup(1, 1, tgSpec{noID: true}))
	if fs := fails(LintDraft(d, nil, nil, nil)); len(fs) != 0 {
		t.Fatalf("findings: %+v", fs)
	}
}

func TestDraftIDThatIsNotVersion7StillFails(t *testing.T) {
	d := draftOf(goodGroup(1, 1, tgSpec{id: "0192e4c5-..."}))
	if fs := rulesOf(LintDraft(d, nil, nil, nil), RuleGrammar, SeverityFail); len(fs) != 1 {
		t.Fatalf("findings: %+v", fs)
	}
}

func TestPlanAssignsAVersion7IDToEachNewGroup(t *testing.T) {
	r := newRig(t, draftOf(
		goodGroup(1, 1, tgSpec{noID: true}),
		goodGroup(1, 2, tgSpec{noID: true, deps: []string{"tg-1.1"}}),
	))
	if _, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g"}); err != nil {
		t.Fatal(err)
	}

	a, b := idLine(t, r, "epic-1/tg-1.1/TG.md"), idLine(t, r, "epic-1/tg-1.2/TG.md")
	if !uuid7.MatchString(a) || !uuid7.MatchString(b) || a == b {
		t.Fatalf("ids %q %q", a, b)
	}
	if epics, fs := mustLoad(t, r.root); len(fs) != 0 || len(epics) != 1 || epics[0].Groups[0].ID != a {
		t.Fatalf("reload: %+v %+v", epics, fs)
	}
}

func TestAmendedGroupKeepsItsStoredID(t *testing.T) {
	r := newRig(t, draftOf(goodGroup(1, 1, tgSpec{noID: true, goal: "Changed"})))
	writeBacklog(t, r.root, goodGroup(1, 1, tgSpec{}))

	if _, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g", Epic: 1}); err != nil {
		t.Fatal(err)
	}
	if got := idLine(t, r, "epic-1/tg-1.1/TG.md"); got != uid(101) {
		t.Fatalf("id %q want %q", got, uid(101))
	}
}

func TestStoredGroupWithoutAnIDStillFailsLoad(t *testing.T) {
	root := t.TempDir()
	writeBacklog(t, root, goodGroup(1, 1, tgSpec{noID: true}))

	_, fs := mustLoad(t, root)
	if got := rulesOf(fs, RuleGrammar, SeverityFail); len(got) != 1 || got[0].Message != "id is missing" {
		t.Fatalf("findings: %+v", fs)
	}
}

func TestDraftIDOfANewGroupIsKept(t *testing.T) {
	r := newRig(t, draftOf(goodGroup(1, 1, tgSpec{})))
	if _, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	if got := idLine(t, r, "epic-1/tg-1.1/TG.md"); got != uid(101) {
		t.Fatalf("id %q", got)
	}
}
