package cli

import (
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/format"
)

func TestBulkApproveReplyNamesTheLandedRiskyGroups(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	planFixture(t, h, repo, samplePlan, "tg-1", "tg-2")
	h.fake(func(_ *fakeServer, _ engine.Request, send func(engine.Reply)) {
		send(okReply(map[string]any{"plan": samplePlan, "answer": "approve", "risky": []string{"tg-1"}}))
	})

	code, e := h.envelope("-C", repo, "run", samplePlan, "--approve")
	if code != 0 || !strings.Contains(e.Report, "landed risky groups: `tg-1`") {
		t.Fatalf("exit %d, report %q", code, e.Report)
	}

	h = newHarness(t)
	h.fake(func(_ *fakeServer, _ engine.Request, send func(engine.Reply)) { send(okReply(map[string]any{})) })
	repo = repoDir(t, "")
	planFixture(t, h, repo, samplePlan, "tg-1")

	if _, e = h.envelope("-C", repo, "run", samplePlan, "--approve"); strings.Contains(e.Report, "risky") {
		t.Fatalf("a reply with no risky groups names some: %q", e.Report)
	}
}

func TestRiskyChangeStatusHintNamesThePlanAndTheGroup(t *testing.T) {
	sd := StatusData{
		Runs:    []RunRow{{RunID: "r1", Target: "plan-aaaaaa"}},
		Blocked: []BlockedRow{{Group: "tg-1", RunID: "r1", Reason: "risky_change", Question: "tg-1: land this risky change?"}},
	}

	facts, ok := blockedResult(sd).Facts.(format.NeedsFacts)
	if want := "komodo run plan-aaaaaa tg-1 --approve or --deny"; !ok || facts.Fix != want {
		t.Fatalf("fix %+v, want %q", facts, want)
	}

	sd.Runs = nil
	if facts, _ = blockedResult(sd).Facts.(format.NeedsFacts); facts.Fix != "komodo run <plan id> tg-1 --approve or --deny" {
		t.Fatalf("fix %q with the run unknown", facts.Fix)
	}
}

func TestPlanReportNamesThePlanIDAndPathAndHowToStartIt(t *testing.T) {
	h := newHarness(t)
	h.fake(goldenReplies)

	_, e := h.envelope("-C", repoDir(t, ""), "plan", "Add lease revocation")
	for _, want := range []string{samplePlan, "/plans/shop/" + samplePlan + "/plan.md", "komodo run " + samplePlan} {
		if !strings.Contains(e.Report, want) {
			t.Fatalf("report %q lacks %q", e.Report, want)
		}
	}
}
