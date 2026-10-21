package task

import (
	"context"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// capRig is a rig with a builder instance and a completion command whose output a test sets per run.
func capRig(t *testing.T) (*pipeRig, *sessCtx) {
	t.Helper()
	r := newPipeRig(t, rigOpts{mut: func(s *GroupSpec, _ *Collaborators) { s.Tasks[0].DoneWhen = "fail" }})
	if _, err := r.m.instance(context.Background(), "builder"); err != nil {
		t.Fatal(err)
	}
	return r, &sessCtx{watch: &toolWatch{limit: 3, now: r.m.now}}
}

// setCheck makes the completion command print text and exit with code.
func setCheck(r *pipeRig, text, code string) {
	r.m.c.Commands["fail"] = []string{"sh", "-c", "echo " + text + "; exit " + code}
}

func TestThreeFailuresWithDifferentErrorsDoNotEndTheSession(t *testing.T) {
	r, sc := capRig(t)
	for _, e := range []string{"a", "b", "c", "d"} {
		setCheck(r, e, "1")
		if _, _, err := r.m.taskDone(context.Background(), sc, "t1"); err != nil {
			t.Fatal(err)
		}
	}
	if got := sc.watch.Halt(); got != "" {
		t.Fatalf("four different errors halted the session with %q", got)
	}
}

func TestThreeSameFailuresInARowEndTheSessionAndKeepTheLastOutput(t *testing.T) {
	r, sc := capRig(t)
	setCheck(r, "boom", "1")
	for range 3 {
		if _, _, err := r.m.taskDone(context.Background(), sc, "t1"); err != nil {
			t.Fatal(err)
		}
	}
	if got := sc.watch.Halt(); got != state.ReasonCheckFail {
		t.Fatalf("halt = %q", got)
	}
	if got := r.m.takeLastCheck(); !strings.Contains(got, "boom") || !strings.Contains(got, "t1") {
		t.Fatalf("last output = %q", got)
	}
	if got := r.m.takeLastCheck(); got != "" {
		t.Fatalf("the last output was not cleared: %q", got)
	}
}

func TestAlternatingPassAndFailIsFlakyAndNeverReachesThree(t *testing.T) {
	r, sc := capRig(t)
	var flaky bool
	for i := range 8 {
		// a pass marks the task done, so each run reopens it to rerun the command
		r.m.mu.Lock()
		r.m.prog.TasksDone = nil
		r.m.mu.Unlock()
		if i%2 == 0 {
			setCheck(r, "boom", "1")
		} else {
			r.m.c.Commands["fail"] = []string{"true"}
		}
		_, out, err := r.m.taskDone(context.Background(), sc, "t1")
		if err != nil {
			t.Fatal(err)
		}
		flaky = flaky || strings.Contains(out, "flaky")
	}
	if got := sc.watch.Halt(); got != "" {
		t.Fatalf("an alternating check halted the session with %q", got)
	}
	if !flaky {
		t.Fatal("the alternating check was not reported flaky")
	}
}

func TestTwentyCheckRunsInOneSessionEndIt(t *testing.T) {
	r, sc := capRig(t)
	for i := range 20 {
		setCheck(r, "err"+strings.Repeat("x", i), "1")
		if got := sc.watch.Halt(); got != "" {
			t.Fatalf("halted after %d runs with %q", i, got)
		}
		if _, _, err := r.m.taskDone(context.Background(), sc, "t1"); err != nil {
			t.Fatal(err)
		}
	}
	if got := sc.watch.Halt(); got != state.ReasonCheckFail {
		t.Fatalf("after 20 runs halt = %q", got)
	}
}

func TestACapEndedSessionPutsTheLastOutputInTheNextBrief(t *testing.T) {
	r := newPipeRig(t, rigOpts{limits: Limits{GroupRetries: 3}})
	r.model.on("builder",
		func(s llm.Session) (llm.Stream, error) {
			r.m.mu.Lock()
			r.m.lastCheck = "the check t1 failed 3 times\nboom-output"
			r.m.mu.Unlock()
			return sayResult(`{"not":"a builder result"}`)(s)
		},
		building(r.wt, "a.go", "package a\n", builderDone))
	r.model.on("qa", sayResult(qaPass))
	r.model.on("reviewer", sayResult(reviewerMet))
	r.reachCleanup()
	if got := r.slot("builder", 1, "findings"); !strings.Contains(got, "boom-output") {
		t.Fatalf("the next builder brief lacks the last check output: %q", got)
	}
	if r.rt.progress().RetriesLeft != 2 {
		t.Fatalf("retries left %d, want 2", r.rt.progress().RetriesLeft)
	}
}
