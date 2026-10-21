package task

import (
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
)

func TestABlockingFindingRaisesLaterRoundsAndKeepsTheReviewer(t *testing.T) {
	docs := "diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1,1 +1,2 @@\n x\n+more\n"
	m := &Manager{c: Collaborators{SkipReview: func(string) bool { return true }}}
	if got := m.classify(docs).Level; got != agent.RiskLow {
		t.Fatalf("a docs change before any blocking finding is %q, want low", got)
	}
	if !m.skipsReview(docs) {
		t.Fatal("a low change before any blocking finding keeps the Reviewer")
	}

	m.noteProven([]VerifiedFinding{{Rule: "quality.1", Bucket: agent.BucketWarn, Confidence: 100}})
	if got := m.classify(docs).Level; got != agent.RiskLow {
		t.Fatalf("a warning raised the level to %q", got)
	}

	m.noteProven([]VerifiedFinding{{Rule: "correctness.1", Bucket: agent.BucketBlock, Confidence: 80}})
	if got := m.classify(docs).Level; got != agent.RiskMedHigh {
		t.Fatalf("a docs change after a blocking finding is %q, want med_high", got)
	}
	if m.skipsReview(docs) {
		t.Fatal("a group with a blocking finding skipped the Reviewer")
	}
}

// A finding that blocks under the proof and confidence rule sets the flag through the reviewer's judgement.
func TestJudgeSetsTheProvenFlagOnlyForABlockingFinding(t *testing.T) {
	m := &Manager{c: Collaborators{}}
	rv := levelRig(t)
	m.noteProven(judgeLevels(t, rv, finding("correctness.2", 2, "var x = bad()", claim, 79)).Verified)
	if m.proven {
		t.Fatal("a finding at confidence 79 set the flag")
	}

	rv = levelRig(t)
	m.noteProven(judgeLevels(t, rv, finding("correctness.2", 2, "var x = bad()", claim, 80)).Verified)
	if !m.proven {
		t.Fatal("a blocking finding did not set the flag")
	}
}
