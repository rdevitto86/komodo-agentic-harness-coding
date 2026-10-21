package task

import (
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
)

func TestAProvenFindingRaisesLaterRoundsAndKeepsTheReviewer(t *testing.T) {
	docs := "diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1,1 +1,2 @@\n x\n+more\n"
	m := &Manager{c: Collaborators{SkipReview: func(string) bool { return true }}}
	if got := m.classify(docs).Level; got != agent.RiskLow {
		t.Fatalf("a docs change before any proof is %q, want low", got)
	}
	if !m.skipsReview(docs) {
		t.Fatal("a low change before any proof keeps the Reviewer")
	}

	m.noteProven([]VerifiedFinding{
		{Rule: "spec.1", Bucket: agent.BucketBlock, Proof: "passed"},
		{Rule: "quality.1", Bucket: agent.BucketWarn, Proof: "failed_as_claimed"},
	})
	if got := m.classify(docs).Level; got != agent.RiskLow {
		t.Fatalf("an unproven or non-blocking finding raised the level to %q", got)
	}

	m.noteProven([]VerifiedFinding{{Rule: "correctness.1", Bucket: agent.BucketBlock, Proof: "failed_as_claimed"}})
	if got := m.classify(docs).Level; got != agent.RiskMedHigh {
		t.Fatalf("a docs change after a proven finding is %q, want med_high", got)
	}
	if m.skipsReview(docs) {
		t.Fatal("a group with a proven finding skipped the Reviewer")
	}
}
