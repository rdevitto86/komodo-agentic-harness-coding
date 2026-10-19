package task

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

func TestPipelineRunsOneGroupThroughBuildTestReviewAndCommit(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	r.model.on("builder", building(r.wt, "a.go", "package a\n", builderDone))
	r.model.on("tester", sayResult(testerPass))
	r.model.on("reviewer", sayResult(reviewerMet))
	r.reachCleanup()

	want := []state.Stage{state.StageBuild, state.StageTestReview, state.StageCommit, state.StageCleanup, state.StageDone}
	if got := r.rt.stagesSeen(); !slices.Equal(got, want) {
		t.Fatalf("stages %v, want %v", got, want)
	}
	if n := r.model.total(); n != 3 {
		t.Fatalf("%d sessions, want 3 (builder, tester, reviewer)", n)
	}
	if n := r.cm.saveCount(); n != 1 {
		t.Fatalf("Save called %d times, want 1", n)
	}
	pubs := r.cm.published()
	if len(pubs) != 1 {
		t.Fatalf("Publish called %d times", len(pubs))
	}
	tree, _ := r.git.TreeHash(context.Background(), r.m.spec.Lease)
	if pubs[0].Tree != tree || pubs[0].Tester.Tree != tree || pubs[0].Reviewer.Tree != tree || pubs[0].Fence != 3 ||
		pubs[0].Branch != "komodo/tg-1" {
		t.Fatalf("approval %+v, tree %s", pubs[0], tree)
	}
	if !slices.Equal(pubs[0].Facts.TasksClosed, []string{"t1"}) || len(pubs[0].Tester.Summary) == 0 ||
		len(pubs[0].Reviewer.Summary) == 0 {
		t.Fatalf("approval facts %+v", pubs[0])
	}
	if len(r.be.released) != 2 {
		t.Fatalf("closing destroyed %d sandbox instances, want 2", len(r.be.released))
	}
	if len(r.be.prepared) != 2 || r.be.prepared[0].Role != "builder" || r.be.prepared[1].Role != "tester" {
		t.Fatalf("instances %+v", r.be.prepared)
	}
	if r.rt.factCount("session.start") != 3 || r.rt.factCount("session.end") != 3 || r.rt.factCount("task.done") != 1 {
		t.Fatalf("facts: %d starts, %d ends", r.rt.factCount("session.start"), r.rt.factCount("session.end"))
	}
	if r.lim.slots[llm.AnthropicAPI] != 0 || len(r.lim.released) != 3 {
		t.Fatalf("slots not released: %v", r.lim.slots)
	}
}

func TestApprovedGroupIsReportedBeforePublish(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	var atPublish int
	r.cm.seen = func() { atPublish = r.rt.factCount("group.approved") }
	r.model.on("builder", building(r.wt, "a.go", "package a\n", builderDone))
	r.model.on("tester", sayResult(testerPass))
	r.model.on("reviewer", sayResult(reviewerMet))
	r.reachCleanup()

	if atPublish != 1 || r.rt.factCount("group.approved") != 1 {
		t.Fatalf("group.approved reported %d times before Publish, %d in all, want 1 and 1", atPublish,
			r.rt.factCount("group.approved"))
	}
}

func TestSessionFolderHoldsTheBriefOnceAndTheRequest(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	r.model.on("builder", building(r.wt, "a.go", "package a\n", builderDone))
	r.model.on("tester", sayResult(testerPass))
	r.model.on("reviewer", sayResult(reviewerMet))
	r.reachCleanup()
	dirs, _ := filepath.Glob(filepath.Join(r.m.c.SessionsDir, "*"))
	if len(dirs) != 3 {
		t.Fatalf("%d session folders", len(dirs))
	}
	for _, d := range dirs {
		for _, f := range []string{"brief.md", "system.md", "scope.json", "request.json", "result.json"} {
			if _, err := os.Stat(filepath.Join(d, f)); err != nil {
				t.Errorf("%s: %v", d, err)
			}
		}
	}
}

func TestNewGroupAddsTheWorktreeOnceAndAContinuedOneNever(t *testing.T) {
	r := newPipeRig(t, rigOpts{
		mut: func(s *GroupSpec, _ *Collaborators) { s.Lease.Worktree = filepath.Join(t.TempDir(), "wt") },
	})
	r.model.on("builder", building(r.wt, "a.go", "package a\n", builderDone))
	r.model.on("tester", sayResult(testerPass))
	r.model.on("reviewer", sayResult(reviewerMet))
	r.reachCleanup()
	if r.git.adds != 1 {
		t.Fatalf("AddWorktree called %d times for a new group", r.git.adds)
	}
	c := newPipeRig(t, rigOpts{saved: &Progress{Stage: state.StageCleanup}})
	c.reachCleanup()
	if c.git.adds != 0 {
		t.Fatalf("AddWorktree called %d times for a continued group", c.git.adds)
	}
}

func TestSavedGroupStartsAtItsStageAndRerunsTestAndReviewBeforeCommit(t *testing.T) {
	for _, stage := range []state.Stage{state.StageTestReview, state.StageCommit} {
		r := newPipeRig(t, rigOpts{saved: &Progress{Stage: stage, TasksDone: []string{"t1"}}})
		r.model.on("tester", sayResult(testerPass))
		r.model.on("reviewer", sayResult(reviewerMet))
		r.reachCleanup()
		if got := r.rt.stagesSeen()[0]; got != state.StageTestReview {
			t.Fatalf("saved %s: first stage %s", stage, got)
		}
		if r.model.started("builder") != 0 || r.model.started("tester") != 1 || r.model.started("reviewer") != 1 {
			t.Fatalf("saved %s: sessions %v", stage, r.model.starts)
		}
	}
}

func TestFailedMechanicalCheckReturnsToBuildWithoutAModelCall(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	r.checks.fail = []bool{true}
	r.model.on("builder",
		building(r.wt, "a.go", "package a\n", builderDone),
		building(r.wt, "b.go", "package b\n", builderDone))
	r.model.on("tester", sayResult(testerPass))
	r.model.on("reviewer", sayResult(reviewerMet))
	r.reachCleanup()
	if got := r.slot("builder", 1, "findings"); !strings.Contains(got, "FAIL: exact output") {
		t.Fatalf("the second builder brief lacks the exact check output: %q", got)
	}
	if r.checks.calls != 2 || r.model.started("builder") != 2 || r.model.started("tester") != 1 ||
		r.model.started("reviewer") != 1 {
		t.Fatalf("checks %d, sessions %v", r.checks.calls, r.model.starts)
	}
	if r.rt.progress().Returns != 1 {
		t.Fatalf("returns %d", r.rt.progress().Returns)
	}
}

func TestReviewerReturnCancelsTheTesterAndSendsTheBuilderTheFindings(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	started := make(chan struct{})
	var tester *fakeStream
	r.model.on("builder",
		building(r.wt, "a.go", "package a\n", builderDone),
		building(r.wt, "b.go", "package b\n", builderDone))
	r.model.on("tester",
		func(llm.Session) (llm.Stream, error) {
			tester = newFakeStream()
			tester.onStop = func(f *fakeStream, _ llm.StopLevel) { f.errs <- context.Canceled }
			close(started)
			return tester, nil
		},
		sayResult(testerPass))
	r.model.on("reviewer",
		func(s llm.Session) (llm.Stream, error) { <-started; return sayResult(reviewerNot)(s) },
		sayResult(reviewerMet))
	r.reachCleanup()
	if got := tester.stopped(); len(got) == 0 || got[0] != llm.Interrupt {
		t.Fatalf("the first tester was not interrupted: %v", got)
	}
	if got := r.slot("builder", 1, "findings"); !strings.Contains(got, "task t1 not met") {
		t.Fatalf("builder findings %q", got)
	}
	if r.rt.progress().Returns != 1 || r.model.started("tester") != 2 {
		t.Fatalf("progress %+v, sessions %v", r.rt.progress(), r.model.starts)
	}
}

func TestTesterAndReviewerReturnsReachTheBuilderInOneTrip(t *testing.T) {
	var sessions string
	r := newPipeRig(t, rigOpts{mut: func(_ *GroupSpec, c *Collaborators) { sessions = c.SessionsDir }})
	r.model.on("builder",
		building(r.wt, "a.go", "package a\n", builderDone),
		building(r.wt, "b.go", "package b\n", builderDone))
	r.model.on("tester", sayResult(testerNoTask), sayResult(testerPass))
	// A reviewer return that lands first cancels the tester, so the reviewer waits for the tester to end.
	r.model.on("reviewer",
		func(s llm.Session) (llm.Stream, error) {
			awaitEnded(t, sessions, "tester")
			return sayResult(reviewerNot)(s)
		},
		sayResult(reviewerMet))
	r.reachCleanup()
	got := r.slot("builder", 1, "findings")
	if !strings.Contains(got, "the tester did not confirm task t1") || !strings.Contains(got, "task t1 not met") {
		t.Fatalf("builder findings %q", got)
	}
	if r.rt.progress().Returns != 1 {
		t.Fatalf("returns %d, want 1", r.rt.progress().Returns)
	}
}

// awaitEnded blocks until a session of the agent has written its result file, the last step of a session.
func awaitEnded(t *testing.T, root, name string) {
	t.Helper()

	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		dirs, _ := os.ReadDir(root)
		for _, d := range dirs {
			raw, err := os.ReadFile(filepath.Join(root, d.Name(), "request.json"))

			var req struct {
				Agent string `json:"agent"`
			}
			if err != nil || json.Unmarshal(raw, &req) != nil || req.Agent != name {
				continue
			}
			if _, err := os.Stat(filepath.Join(root, d.Name(), "result.json")); err == nil {
				return
			}
		}
	}

	t.Errorf("no %s session ended in 10 seconds", name)
}

func TestReturnWithAnUnchangedTreeEscalatesFlap(t *testing.T) {
	r := newPipeRig(t, rigOpts{limits: Limits{Returns: 4}})
	r.model.on("builder", building(r.wt, "a.go", "package a\n", builderDone), sayResult(builderDone))
	r.model.on("tester", sayResult(testerPass), sayResult(testerPass))
	r.model.on("reviewer", sayResult(reviewerNot), sayResult(reviewerNot))
	err := r.run()
	if err != ErrHeld {
		t.Fatalf("Run returned %v", err)
	}
	escs := r.rt.escalations()
	if len(escs) != 1 || escs[0].Reason != "flap" || escs[0].Kind != EscBlocked {
		t.Fatalf("escalations %+v", escs)
	}
}

func TestReturnsAtTheLimitEscalateReturns(t *testing.T) {
	r := newPipeRig(t, rigOpts{limits: Limits{Returns: 1}})
	r.model.on(
		"builder",
		building(r.wt, "a.go", "package a\n", builderDone),
		building(r.wt, "b.go", "package b\n", builderDone),
	)
	r.model.on("tester", sayResult(testerPass), sayResult(testerPass))
	r.model.on("reviewer", sayResult(reviewerNot), sayResult(reviewerNot))
	if err := r.run(); err != ErrHeld {
		t.Fatalf("Run returned %v", err)
	}
	if escs := r.rt.escalations(); len(escs) != 1 || escs[0].Reason != "returns" {
		t.Fatalf("escalations %+v", escs)
	}
}

func TestDisputedTesterResultRestartsTheTesterOnceAndASecondEscalatesFlaky(t *testing.T) {
	items := []agent.Item{{ID: "a1", Text: "it works", Cmd: "test"}}
	failed := `{"items":[{"item":"a1","pass":false,"evidence":"it failed"}],"tasks":[{"task":"t1",` +
		`"confirmation":"confirmed"}]}`
	passed := `{"items":[{"item":"a1","pass":true,"evidence":"ok"}],"tasks":[{"task":"t1","confirmation":"confirmed"}]}`

	r := newPipeRig(t, rigOpts{items: items})
	r.model.on("builder", building(r.wt, "a.go", "package a\n", builderDone))
	r.model.on("tester", sayResult(failed), sayResult(passed))
	r.model.on("reviewer", sayResult(reviewerMet))
	r.reachCleanup()
	if r.model.started("tester") != 2 || r.model.started("builder") != 1 || r.rt.progress().Disputes != 1 {
		t.Fatalf("sessions %v, progress %+v", r.model.starts, r.rt.progress())
	}
	if got := r.slot("tester", 1, "checks"); !strings.Contains(got, "Go reran item a1") {
		t.Fatalf("the restart brief lacks Go's output: %q", got)
	}

	f := newPipeRig(t, rigOpts{items: items})
	f.model.on("builder", building(f.wt, "a.go", "package a\n", builderDone))
	f.model.on("tester", sayResult(failed), sayResult(failed))
	f.model.on("reviewer", sayResult(reviewerMet))
	if err := f.run(); err != ErrHeld {
		t.Fatalf("Run returned %v", err)
	}
	if escs := f.rt.escalations(); len(escs) != 1 || escs[0].Reason != "tester" {
		t.Fatalf("escalations %+v", escs)
	}
	if f.model.started("builder") != 1 {
		t.Fatal("a disputed result sent the group back to Build")
	}
}

func TestTrackedEditByAJudgeIsResetOnceAndTheSecondEscalatesContract(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	r.git.statuses = []string{"", " M a.go\n", " M a.go\n"}
	r.model.on("builder", building(r.wt, "a.go", "package a\n", builderDone))
	r.model.on("tester", sayResult(testerPass), sayResult(testerPass))
	r.model.on("reviewer", sayResult(reviewerMet), sayResult(reviewerMet))
	if err := r.run(); err != ErrHeld {
		t.Fatalf("Run returned %v", err)
	}
	if escs := r.rt.escalations(); len(escs) != 1 || escs[0].Reason != "contract" {
		t.Fatalf("escalations %+v", escs)
	}
	if r.git.resets != 2 || r.model.started("reviewer") != 2 {
		t.Fatalf("resets %d, reviewer sessions %d", r.git.resets, r.model.started("reviewer"))
	}
}

func TestPassingTaskDoneSavesOnceAndANewTaskNeverPushes(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	if _, err := r.m.instance(context.Background(), "builder"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		exit, _, err := r.m.finishTask(context.Background(), "t1")
		if err != nil || exit != 0 {
			t.Fatalf("finishTask: %d, %v", exit, err)
		}
	}
	if r.cm.saveCount() != 1 || !slices.Equal(r.m.Progress().TasksDone, []string{"t1"}) {
		t.Fatalf("saves %d, done %v", r.cm.saveCount(), r.m.Progress().TasksDone)
	}
	if s := r.cm.saves[0]; s.Task != "t1" || s.Repo != "repo-1" || s.Branch != "komodo/tg-1" || s.Fence != 3 {
		t.Fatalf("save request %+v", s)
	}
}

func TestFailingCompletionCommandKeepsTheTaskOpenAndEndsTheSessionAfterThree(t *testing.T) {
	r := newPipeRig(t, rigOpts{mut: func(s *GroupSpec, _ *Collaborators) { s.Tasks[0].DoneWhen = "fail" }})
	if _, err := r.m.instance(context.Background(), "builder"); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan state.Reason, 1)
	sc := &sessCtx{watch: &toolWatch{limit: 3, now: r.m.now}}
	_ = stopped
	for i := 0; i < 3; i++ {
		exit, _, err := r.m.taskDone(context.Background(), sc, "t1")
		if err != nil || exit == 0 {
			t.Fatalf("taskDone %d: %d, %v", i, exit, err)
		}
	}
	if r.cm.saveCount() != 0 || len(r.m.Progress().TasksDone) != 0 {
		t.Fatal("a failing task was saved")
	}
	if got := sc.watch.Halt(); got != state.ReasonCheckFail {
		t.Fatalf("after three failures the session halts with %q", got)
	}
}

func TestBuilderSessionsEndTheGroupAfterTheAttemptLimit(t *testing.T) {
	r := newPipeRig(t, rigOpts{
		limits: Limits{Attempts: 2},
		mut:    func(s *GroupSpec, _ *Collaborators) { s.Tasks[0].DoneWhen = "fail" },
	})
	r.model.on("builder", sayResult(builderDone), sayResult(builderDone))
	if err := r.run(); err != ErrHeld {
		t.Fatalf("Run returned %v", err)
	}
	if escs := r.rt.escalations(); len(escs) != 1 || escs[0].Reason != "attempts" {
		t.Fatalf("escalations %+v", escs)
	}
	if r.rt.progress().Attempts != 2 || r.cm.saveCount() != 0 {
		t.Fatalf("progress %+v", r.rt.progress())
	}
}

func TestUnfinishedBuilderSessionReportsTheChangedFiles(t *testing.T) {
	r := newPipeRig(t, rigOpts{limits: Limits{Attempts: 1}})
	r.git.status = " M a.go\n?? b.go\n"
	r.model.on("builder", sayResult(`{"items":[]}`))
	if err := r.run(); err != ErrHeld {
		t.Fatalf("Run returned %v", err)
	}
	var found bool
	r.rt.mu.Lock()
	for _, f := range r.rt.facts {
		if f.Subtype == "session.end" && strings.Contains(string(f.Data), `"unfinished"`) &&
			strings.Contains(string(f.Data), "b.go") {
			found = true
		}
	}
	r.rt.mu.Unlock()
	if !found {
		t.Fatal("session.end carries no unfinished list")
	}
	if r.cm.saveCount() != 0 {
		t.Fatal("the Task Manager committed unfinished work")
	}
}
