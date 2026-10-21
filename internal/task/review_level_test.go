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
func levelRig(t *testing.T, run agent.ProofRunner) *reviewerRole {
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
		Files: func() []string { return []string{"*.go"} }, Reviewer: rev, Proof: run,
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

func finding(rule string, line int, quote string, p *agent.Proof) Submitted {
	return Submitted{Finding: agent.Finding{Rule: rule, File: "a.go", Line: line, Quote: quote, Proof: p}, Note: "n"}
}

func failing() agent.ProofRunner {
	return func(context.Context, agent.Proof) (agent.ProofResult, error) {
		return agent.ProofResult{Failed: true, Output: "--- FAIL: TestBad"}, nil
	}
}

var claim = &agent.Proof{Cmd: "go test ./... -run TestBad", Expect: "--- FAIL: TestBad"}

func TestReviewerJudgeReturnsAFindingWhoseProofFailsAsClaimed(t *testing.T) {
	rv := levelRig(t, failing())
	out := judgeLevels(t, rv, finding("correctness.2", 2, "var x = bad()", claim))
	if out.Kind != OutcomeReturn || len(out.Findings) != 1 || !strings.Contains(out.Findings[0], "correctness.2") {
		t.Fatalf("outcome = %+v", out)
	}
	if len(out.Verified) != 1 || out.Verified[0].Level != agent.LevelMedHigh ||
		out.Verified[0].Proof != "failed_as_claimed" {
		t.Errorf("verified = %+v", out.Verified)
	}
}

func TestReviewerJudgeWarnsOnABlockingFindingWithoutAFailingProof(t *testing.T) {
	passing := func(context.Context, agent.Proof) (agent.ProofResult, error) {
		return agent.ProofResult{Output: "ok"}, nil
	}
	cases := map[string]struct {
		run agent.ProofRunner
		p   *agent.Proof
	}{
		"proof passes": {passing, claim},
		"no proof":     {failing(), nil},
		"unlisted command": {func(context.Context, agent.Proof) (agent.ProofResult, error) {
			return agent.ProofResult{}, agent.ErrProofRefused
		}, claim},
		"no proof runner": {nil, claim},
		"other failure out": {func(context.Context, agent.Proof) (agent.ProofResult, error) {
			return agent.ProofResult{Failed: true, Output: "boom"}, nil
		}, claim},
	}
	for name, c := range cases {
		rv := levelRig(t, c.run)
		out := judgeLevels(t, rv, finding("correctness.2", 2, "var x = bad()", c.p))
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
	rv := levelRig(t, failing())
	out := judgeLevels(t, rv,
		finding("quality.1", 2, "var x = bad()", nil),
		finding("std.a.2", 3, "var y = worse()", nil),
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
	rv := levelRig(t, func(context.Context, agent.Proof) (agent.ProofResult, error) {
		t.Error("the proof of a dropped finding ran")
		return agent.ProofResult{}, nil
	})
	// line 1 is context in the diff; the quote is right, but the group did not change it
	out := judgeLevels(t, rv,
		finding("correctness.2", 1, "package a", claim),
		finding("quality.1", 1, "package a", nil),
	)
	if out.Kind != OutcomePass || len(out.Verified) != 0 || len(out.Recorded) != 0 {
		t.Fatalf("outcome = %+v", out)
	}
	if sum := strings.Join(out.Summary, "\n"); strings.Contains(sum, "warning") {
		t.Errorf("summary = %q, want no warning", sum)
	}
}
