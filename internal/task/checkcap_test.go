package task

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
)

// capRig is a rig with a builder instance and a completion command whose output a test sets per run.
func capRig(t *testing.T) (*pipeRig, *sessCtx) {
	t.Helper()
	r := newPipeRig(t, rigOpts{
		limits: Limits{GroupRetries: 3},
		mut:    func(s *GroupSpec, _ *Collaborators) { s.Tasks[0].DoneWhen = "fail" },
	})
	if _, err := r.m.instance(context.Background(), "builder"); err != nil {
		t.Fatal(err)
	}
	return r, &sessCtx{watch: &toolWatch{limit: 3, now: r.m.now}}
}

// setCheck makes the completion command print text and exit with code.
func setCheck(r *pipeRig, text, code string) {
	r.m.c.Commands["fail"] = []string{"sh", "-c", "echo " + text + "; exit " + code}
}

// crashedBuilder is a builder session whose process dies mid-run.
func crashedBuilder(llm.Session) (llm.Stream, error) {
	st := newFakeStream()
	st.ev <- msgEvent("m1", 3, 20)
	crashAfter(st, io.ErrUnexpectedEOF)
	return st, nil
}

func TestFailingChecksNeverEndTheSessionOrSpendATry(t *testing.T) {
	r, sc := capRig(t)
	setCheck(r, "boom", "1")
	for range 30 {
		if exit, _, err := r.m.taskDone(context.Background(), sc, "t1"); err != nil || exit == 0 {
			t.Fatalf("taskDone: %d, %v", exit, err)
		}
		if got := sc.watch.Halt(); got != "" {
			t.Fatalf("a failing check halted the session with %q", got)
		}
	}
	r.m.c.Commands["fail"] = []string{"true"}
	if exit, _, err := r.m.taskDone(context.Background(), sc, "t1"); err != nil || exit != 0 {
		t.Fatalf("the passing run: %d, %v", exit, err)
	}
	if got := sc.watch.Halt(); got != "" {
		t.Fatalf("halt = %q", got)
	}
	if got := r.rt.progress().RetriesLeft; got != 3 {
		t.Fatalf("retries left %d, want 3", got)
	}
}

func TestTheLastFailingOutputIsKeptForTheNextTry(t *testing.T) {
	r, sc := capRig(t)
	setCheck(r, "boom", "1")
	if _, _, err := r.m.taskDone(context.Background(), sc, "t1"); err != nil {
		t.Fatal(err)
	}
	if got := r.m.takeLastCheck(); !strings.Contains(got, "boom") || !strings.Contains(got, "t1") {
		t.Fatalf("last output = %q", got)
	}
	if got := r.m.takeLastCheck(); got != "" {
		t.Fatalf("the last output was not cleared: %q", got)
	}
}

func TestAPassClearsTheKeptOutput(t *testing.T) {
	r, sc := capRig(t)
	setCheck(r, "boom", "1")
	if _, _, err := r.m.taskDone(context.Background(), sc, "t1"); err != nil {
		t.Fatal(err)
	}
	r.m.c.Commands["fail"] = []string{"true"}
	if _, _, err := r.m.taskDone(context.Background(), sc, "t1"); err != nil {
		t.Fatal(err)
	}
	if got := r.m.takeLastCheck(); got != "" {
		t.Fatalf("a pass left %q", got)
	}
}

func TestAKeptOutputGoesInTheNextBrief(t *testing.T) {
	r := newPipeRig(t, rigOpts{limits: Limits{GroupRetries: 3}})
	r.model.on("builder",
		func(s llm.Session) (llm.Stream, error) {
			r.m.mu.Lock()
			r.m.lastCheck = "the check t1 failed\nboom-output"
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

func TestACrashSpendsOneTry(t *testing.T) {
	r := newPipeRig(t, rigOpts{limits: Limits{GroupRetries: 3}})
	r.model.on("builder", crashedBuilder, building(r.wt, "a.go", "package a\n", builderDone))
	r.model.on("qa", sayResult(qaPass))
	r.model.on("reviewer", sayResult(reviewerMet))
	r.reachCleanup()
	if r.rt.progress().RetriesLeft != 2 {
		t.Fatalf("retries left %d, want 2", r.rt.progress().RetriesLeft)
	}
}

func TestThreeCrashesBlockRetries(t *testing.T) {
	r := newPipeRig(t, rigOpts{limits: Limits{GroupRetries: 3}})
	r.model.on("builder", crashedBuilder, crashedBuilder, crashedBuilder)
	if err := r.run(); err != ErrHeld {
		t.Fatalf("Run returned %v", err)
	}
	escs := r.rt.escalations()
	if len(escs) != 1 || escs[0].Reason != "retries" {
		t.Fatalf("escalations %+v", escs)
	}
	if r.rt.progress().RetriesLeft != 0 {
		t.Fatalf("progress %+v", r.rt.progress())
	}
}
