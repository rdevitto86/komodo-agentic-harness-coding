package task

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
)

// levelRig builds a reviewer role over a worktree holding a.go, with a diff that adds its lines 2 and 3 only.
func levelRig(t *testing.T) *reviewerRole {
	t.Helper()
	std := func(id string) (agent.FindingLevel, bool) {
		levels := map[string]agent.FindingLevel{"std.a.1": agent.LevelMedium, "std.a.2": agent.LevelMedLow}
		return levels[id], strings.HasPrefix(id, "std.a.")
	}
	rev, err := agent.NewReviewer(std)
	if err != nil {
		t.Fatal(err)
	}

	wt := t.TempDir()
	src := []byte("package a\nvar x = bad()\nvar y = worse()\n")
	if err := os.WriteFile(filepath.Join(wt, "a.go"), src, 0o644); err != nil {
		t.Fatal(err)
	}

	roles, err := NewRoles(RoleConfig{
		Group: "tg-1", Tasks: []TaskSpec{{ID: "t1", Title: "x", DoneWhen: "build"}}, Worktree: wt,
		Files: func() []string { return []string{"*.go"} }, Reviewer: rev,
	})
	if err != nil {
		t.Fatal(err)
	}

	rv := roles["reviewer"].(*reviewerRole)
	diff := "--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,3 @@\n package a\n+var x = bad()\n+var y = worse()\n"
	if _, err := rv.Slots(context.Background(), RoleInput{Diff: diff}); err != nil {
		t.Fatal(err)
	}
	return rv
}

// judgeLevels files the findings with a met review and returns the outcome.
func judgeLevels(t *testing.T, rv *reviewerRole, fs ...Submitted) Outcome {
	t.Helper()
	for _, f := range fs {
		rv.Submit(f)
	}
	out, err := rv.Judge(context.Background(), result(`{"verdicts":[{"task":"t1","verdict":"met"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func finding(rule string, line int, quote string, p *agent.Proof, confidence int) Submitted {
	return Submitted{
		Finding: agent.Finding{Rule: rule, File: "a.go", Line: line, Quote: quote, Proof: p, Confidence: confidence},
		Note:    "n",
	}
}

var claim = &agent.Proof{Ran: "go test ./... -run TestBad", Saw: "--- FAIL: TestBad"}

func TestReviewerJudgeReturnsAFindingWithAProofAtConfidence80(t *testing.T) {
	rv := levelRig(t)
	out := judgeLevels(t, rv, finding("correctness.2", 2, "var x = bad()", claim, 80))
	if out.Kind != OutcomeReturn || len(out.Findings) != 1 || !strings.Contains(out.Findings[0], "correctness.2") {
		t.Fatalf("outcome = %+v", out)
	}
	v := out.Verified
	if len(v) != 1 || v[0].Level != agent.LevelMedHigh || v[0].Confidence != 80 || v[0].Proof == nil {
		t.Errorf("verified = %+v", v)
	}
}

// A rule no command could prove, such as readiness.1, blocks by its level like any other rule.
func TestReviewerJudgeBlocksAFormerlyUnprovableRule(t *testing.T) {
	rv := levelRig(t)
	proof := &agent.Proof{Ran: "read the diff", Saw: "the failure is dropped"}
	out := judgeLevels(t, rv, finding("readiness.1", 2, "var x = bad()", proof, 80))
	if out.Kind != OutcomeReturn || len(out.Findings) != 1 {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestReviewerJudgeWarnsOnABlockingFindingBelowConfidence80OrWithoutAProof(t *testing.T) {
	cases := map[string]struct {
		p    *agent.Proof
		conf int
	}{
		"confidence 79":      {claim, 79},
		"no proof":           {nil, 100},
		"proof saw nothing":  {&agent.Proof{Ran: "go vet"}, 100},
		"confidence 0":       {claim, 0},
		"proof at 100 holds": {claim, 100},
	}
	for name, c := range cases {
		rv := levelRig(t)
		out := judgeLevels(t, rv, finding("correctness.2", 2, "var x = bad()", c.p, c.conf))
		if name == "proof at 100 holds" {
			if out.Kind != OutcomeReturn {
				t.Errorf("%s: outcome = %+v", name, out)
			}
			continue
		}
		if out.Kind != OutcomePass || len(out.Findings) != 0 {
			t.Errorf("%s: outcome = %+v", name, out)
			continue
		}
		if len(out.Summary) < 2 || !strings.Contains(strings.Join(out.Summary, "\n"), "warning: correctness.2") {
			t.Errorf("%s: summary lacks the warning: %v", name, out.Summary)
		}
	}
}

func TestReviewerJudgeWarnsOnAMediumAndRecordsALowerLevel(t *testing.T) {
	rv := levelRig(t)
	out := judgeLevels(t, rv,
		finding("quality.1", 2, "var x = bad()", claim, 100),
		finding("std.a.2", 3, "var y = worse()", nil, 100),
	)
	if out.Kind != OutcomePass {
		t.Fatalf("outcome = %+v", out)
	}
	sum := strings.Join(out.Summary, "\n")
	if !strings.Contains(sum, "warning: quality.1") || strings.Contains(sum, "std.a.2") {
		t.Errorf("summary = %q: want the medium warning and no med_low line", sum)
	}
	if len(out.Recorded) != 1 || !strings.Contains(out.Recorded[0], "std.a.2") {
		t.Errorf("recorded = %v", out.Recorded)
	}
	if len(out.Verified) != 2 {
		t.Errorf("verified = %+v, want both findings", out.Verified)
	}
}

func TestReviewerJudgeDropsAFindingOnALineTheGroupDidNotChange(t *testing.T) {
	rv := levelRig(t)
	// line 1 is context in the diff; the quote is right, but the group did not change it
	out := judgeLevels(t, rv,
		finding("correctness.2", 1, "package a", claim, 100),
		finding("quality.1", 1, "package a", nil, 100),
	)
	if out.Kind != OutcomePass || len(out.Verified) != 0 || len(out.Recorded) != 0 {
		t.Fatalf("outcome = %+v", out)
	}
	if sum := strings.Join(out.Summary, "\n"); strings.Contains(sum, "warning") {
		t.Errorf("summary = %q, want no warning", sum)
	}
}
