package intake

import "testing"

func TestStoredGroupIDs(t *testing.T) {
	t.Parallel()

	t.Run("a stored group without an id is a grammar finding", func(t *testing.T) {
		t.Parallel()

		b := bpPlan(bpGroup("tg-1"), bpGroup("tg-2"))
		b.Groups[0].ID = ""

		got := only(t, Lint(b, nil, nil), RuleGrammar, SeverityFail, 1)
		if got[0].Path != "plan.md#tg-1" || got[0].Message != "id is missing" {
			t.Fatalf("finding: %+v", got[0])
		}
	})

	t.Run("a draft group without an id passes", func(t *testing.T) {
		t.Parallel()

		if fs := fails(LintDraft(newDraft(bpGroup("tg-1")), Blueprint{}, nil, nil)); len(fs) != 0 {
			t.Fatalf("findings: %s", findingText(fs))
		}
	})

	t.Run("a draft id that is not version 7 fails", func(t *testing.T) {
		t.Parallel()

		g := bpGroup("tg-1")
		g.ID = "0192e4c5-..."
		only(t, LintDraft(newDraft(g), Blueprint{}, nil, nil), RuleGrammar, SeverityFail, 1)
	})
}
