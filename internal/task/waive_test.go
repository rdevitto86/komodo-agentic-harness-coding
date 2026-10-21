package task

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
)

// qaUntestable is Quality Assurance's result when it could not make the environment ready.
const qaUntestable = `{"status":"untestable","cause":"docker did not start after 2 attempts","items":[],"tasks":[]}`

func crashing(llm.Session) (llm.Stream, error) { return nil, errors.New("the process died") }

// warningsOf returns the Quality Assurance summary lines of the one published approval.
func warningsOf(t *testing.T, r *pipeRig) string {
	t.Helper()
	pubs := r.cm.published()
	if len(pubs) != 1 {
		t.Fatalf("published %d approvals, want 1", len(pubs))
	}
	return strings.Join(pubs[0].QA.Summary, "\n")
}

// One Quality Assurance crash runs it again; no retry is spent and nothing fails open.
func TestOneQACrashRerunsQAAndSpendsNoTry(t *testing.T) {
	r := newPipeRig(t, rigOpts{limits: Limits{GroupRetries: 2}})
	r.model.on("builder", building(r.wt, "a.go", "package a\n", builderDone))
	r.model.on("qa", crashing, sayResult(qaPass))
	r.model.on("reviewer", sayResult(reviewerMet))
	r.reachCleanup()

	if got := r.rt.progress().RetriesLeft; got != 2 || r.model.started("qa") != 2 {
		t.Fatalf("retries left %d, sessions %v", got, r.model.starts)
	}
	if n := r.rt.factCount("qa.fail_open"); n != 0 {
		t.Fatalf("%d qa.fail_open facts after one crash", n)
	}
	if _, ok := r.m.Signoff(); !ok {
		t.Error("a rerun that passed gave no sign-off")
	}
}

// Two Quality Assurance crashes in a row fail open: an event, a pull request warning, no sign-off and no spent try.
func TestTwoQACrashesInARowFailOpen(t *testing.T) {
	r := newPipeRig(t, rigOpts{limits: Limits{GroupRetries: 2}})
	r.model.on("builder", building(r.wt, "a.go", "package a\n", builderDone))
	r.model.on("qa", crashing, crashing, sayResult(qaPass))
	r.model.on("reviewer", sayResult(reviewerMet))
	r.reachCleanup()

	if got := r.rt.progress().RetriesLeft; got != 2 || r.model.started("qa") != 2 {
		t.Fatalf("retries left %d, sessions %v", got, r.model.starts)
	}
	if n := r.rt.factCount("qa.fail_open"); n != 1 {
		t.Fatalf("%d qa.fail_open facts, want 1", n)
	}
	if w := warningsOf(t, r); !strings.Contains(w, "not tested by Quality Assurance: ") || !strings.Contains(w, "crash") {
		t.Errorf("pull request summary = %q", w)
	}
	if _, ok := r.m.Signoff(); ok {
		t.Error("a group that failed open was signed off")
	}
}

// An untestable result fails open at once, with the cause in the event and the warning.
func TestUntestableQAFailsOpenAtOnce(t *testing.T) {
	r := newPipeRig(t, rigOpts{limits: Limits{GroupRetries: 2}})
	r.model.on("builder", building(r.wt, "a.go", "package a\n", builderDone))
	r.model.on("qa", sayResult(qaUntestable))
	r.model.on("reviewer", sayResult(reviewerMet))
	r.reachCleanup()

	if got := r.rt.progress().RetriesLeft; got != 2 || r.model.started("qa") != 1 {
		t.Fatalf("retries left %d, sessions %v", got, r.model.starts)
	}
	if n := r.rt.factCount("qa.fail_open"); n != 1 {
		t.Fatalf("%d qa.fail_open facts, want 1", n)
	}
	if w := warningsOf(t, r); !strings.Contains(w, "not tested by Quality Assurance: docker did not start") {
		t.Errorf("pull request summary = %q", w)
	}
	if _, ok := r.m.Signoff(); ok {
		t.Error("an untestable group was signed off")
	}
}

// A Reviewer crash spends one try and the Reviewer runs again; it never fails open.
func TestReviewerCrashSpendsOneTry(t *testing.T) {
	r := newPipeRig(t, rigOpts{limits: Limits{GroupRetries: 3}})
	r.model.on("builder", building(r.wt, "a.go", "package a\n", builderDone))
	r.model.on("qa", sayResult(qaPass))
	r.model.on("reviewer", crashing, sayResult(reviewerMet))
	r.reachCleanup()

	if got := r.rt.progress().RetriesLeft; got != 2 || r.model.started("reviewer") != 2 {
		t.Fatalf("retries left %d, sessions %v", got, r.model.starts)
	}
	if n := r.rt.factCount("qa.fail_open"); n != 0 {
		t.Fatalf("%d qa.fail_open facts", n)
	}
	if !slices.Contains(r.rt.stagesSeen(), "cleanup") {
		t.Errorf("stages %v", r.rt.stagesSeen())
	}
}
