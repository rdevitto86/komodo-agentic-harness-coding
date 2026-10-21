package task

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/sandbox"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

func TestPipelineRunsOneGroupThroughBuildTestReviewAndCommit(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	r.model.on("builder", building(r.wt, "a.go", "package a\n", builderDone))
	r.model.on("qa", sayResult(qaPass))
	r.model.on("reviewer", sayResult(reviewerMet))
	r.reachCleanup()

	want := []state.Stage{state.StageBuild, state.StageTestReview, state.StageCommit, state.StageCleanup, state.StageDone}
	if got := r.rt.stagesSeen(); !slices.Equal(got, want) {
		t.Fatalf("stages %v, want %v", got, want)
	}
	if n := r.model.total(); n != 3 {
		t.Fatalf("%d sessions, want 3 (builder, Quality Assurance, reviewer)", n)
	}
	if n := r.cm.saveCount(); n != 1 {
		t.Fatalf("Save called %d times, want 1", n)
	}
	pubs := r.cm.published()
	if len(pubs) != 1 {
		t.Fatalf("Publish called %d times", len(pubs))
	}
	tree, _ := r.git.TreeHash(context.Background(), r.m.spec.Lease)
	if pubs[0].Tree != tree || pubs[0].QA.Tree != tree || pubs[0].Reviewer.Tree != tree || pubs[0].Fence != 3 ||
		pubs[0].Branch != "komodo/tg-1" {
		t.Fatalf("approval %+v, tree %s", pubs[0], tree)
	}
	if !slices.Equal(pubs[0].Facts.TasksClosed, []string{"t1"}) || len(pubs[0].QA.Summary) == 0 ||
		len(pubs[0].Reviewer.Summary) == 0 {
		t.Fatalf("approval facts %+v", pubs[0])
	}
	if len(r.be.released) != 2 {
		t.Fatalf("closing destroyed %d sandbox instances, want 2", len(r.be.released))
	}
	if len(r.be.prepared) != 2 || r.be.prepared[0].Role != "builder" || r.be.prepared[1].Role != "qa" {
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
	r.model.on("qa", sayResult(qaPass))
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
	r.model.on("qa", sayResult(qaPass))
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
	r.model.on("qa", sayResult(qaPass))
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
		r.model.on("qa", sayResult(qaPass))
		r.model.on("reviewer", sayResult(reviewerMet))
		r.reachCleanup()
		if got := r.rt.stagesSeen()[0]; got != state.StageTestReview {
			t.Fatalf("saved %s: first stage %s", stage, got)
		}
		if r.model.started("builder") != 0 || r.model.started("qa") != 1 || r.model.started("reviewer") != 1 {
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
	r.model.on("qa", sayResult(qaPass))
	r.model.on("reviewer", sayResult(reviewerMet))
	r.reachCleanup()
	if got := r.slot("builder", 1, "findings"); !strings.Contains(got, "FAIL: exact output") {
		t.Fatalf("the second builder brief lacks the exact check output: %q", got)
	}
	if r.checks.calls != 2 || r.model.started("builder") != 2 || r.model.started("qa") != 1 ||
		r.model.started("reviewer") != 1 {
		t.Fatalf("checks %d, sessions %v", r.checks.calls, r.model.starts)
	}
	if r.rt.progress().Returns != 1 {
		t.Fatalf("returns %d", r.rt.progress().Returns)
	}
}

func TestReviewerReturnCancelsTheQAAndSendsTheBuilderTheFindings(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	started := make(chan struct{})
	var qa *fakeStream
	r.model.on("builder",
		building(r.wt, "a.go", "package a\n", builderDone),
		building(r.wt, "b.go", "package b\n", builderDone))
	r.model.on("qa",
		func(llm.Session) (llm.Stream, error) {
			qa = newFakeStream()
			qa.onStop = func(f *fakeStream, _ llm.StopLevel) { f.errs <- context.Canceled }
			close(started)
			return qa, nil
		},
		sayResult(qaPass))
	r.model.on("reviewer",
		func(s llm.Session) (llm.Stream, error) { <-started; return sayResult(reviewerNot)(s) },
		sayResult(reviewerMet))
	r.reachCleanup()
	if got := qa.stopped(); len(got) == 0 || got[0] != llm.Interrupt {
		t.Fatalf("the first Quality Assurance was not interrupted: %v", got)
	}
	if got := r.slot("builder", 1, "findings"); !strings.Contains(got, "task t1 not met") {
		t.Fatalf("builder findings %q", got)
	}
	if r.rt.progress().Returns != 1 || r.model.started("qa") != 2 {
		t.Fatalf("progress %+v, sessions %v", r.rt.progress(), r.model.starts)
	}
}

func TestQAAndReviewerReturnsReachTheBuilderInOneTrip(t *testing.T) {
	var sessions string
	r := newPipeRig(t, rigOpts{mut: func(_ *GroupSpec, c *Collaborators) { sessions = c.SessionsDir }})
	r.model.on("builder",
		building(r.wt, "a.go", "package a\n", builderDone),
		building(r.wt, "b.go", "package b\n", builderDone))
	r.model.on("qa", sayResult(qaNoTask), sayResult(qaPass))
	// A reviewer return that lands first cancels Quality Assurance, so the reviewer waits for Quality Assurance to end.
	r.model.on("reviewer",
		func(s llm.Session) (llm.Stream, error) {
			awaitEnded(t, sessions, "qa")
			return sayResult(reviewerNot)(s)
		},
		sayResult(reviewerMet))
	r.reachCleanup()
	got := r.slot("builder", 1, "findings")
	if !strings.Contains(got, "Quality Assurance did not confirm task t1") || !strings.Contains(got, "task t1 not met") {
		t.Fatalf("builder findings %q", got)
	}
	if r.rt.progress().Returns != 1 {
		t.Fatalf("returns %d, want 1", r.rt.progress().Returns)
	}
}

func TestJudgeSessionEndsAreReportedInTheStageTheyBelongTo(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	r.model.on("builder",
		building(r.wt, "a.go", "package a\n", builderDone),
		building(r.wt, "b.go", "package b\n", builderDone))
	r.model.on("qa", sayResult(qaPass), sayResult(qaPass))
	r.model.on("reviewer", sayResult(reviewerNot), sayResult(reviewerMet))
	r.reachCleanup()

	r.rt.mu.Lock()
	sent := slices.Clone(r.rt.sent)
	r.rt.mu.Unlock()

	judgeEnds := 0
	for _, s := range sent {
		var judges, fail bool
		for _, f := range s.Facts {
			var d struct {
				Agent string `json:"agent"`
			}
			_ = json.Unmarshal(f.Data, &d)
			judges = judges || f.Subtype == "session.end" && (d.Agent == "qa" || d.Agent == "reviewer")
			fail = fail || f.Subtype == "result.fail"
		}
		if judges {
			judgeEnds++
			if s.Progress.Stage == state.StageBuild || fail {
				t.Fatalf("judge session.end sent with stage %s and result.fail %v", s.Progress.Stage, fail)
			}
		}
	}
	if judgeEnds == 0 {
		t.Fatal("no report carried a judge session.end")
	}
}

// dockerDown is a Quality Assurance mechanism whose check fails, so Quality Assurance runs on the fallback.
type dockerDown struct{ *hostBackend }

func (dockerDown) Name() string { return "docker" }

func (dockerDown) Check() error { return errors.New("the docker daemon does not answer: start Docker") }

func TestSandboxFallbackFactCarriesTheWhyTheEventDeclares(t *testing.T) {
	r := newPipeRig(t, rigOpts{mut: func(_ *GroupSpec, c *Collaborators) {
		host := &hostBackend{}
		c.Sandboxes = sandbox.NewManager(map[string]sandbox.Backend{"builder": host, "qa": dockerDown{host}}, host)
	}})
	r.model.on("builder", building(r.wt, "a.go", "package a\n", builderDone))
	r.model.on("qa", sayResult(qaPass))
	r.model.on("reviewer", sayResult(reviewerMet))
	r.reachCleanup()

	r.rt.mu.Lock()
	facts := slices.Clone(r.rt.facts)
	r.rt.mu.Unlock()

	for _, f := range facts {
		if f.Subtype != "sandbox.fallback" {
			continue
		}

		var d bus.SandboxFallback
		if err := json.Unmarshal(f.Data, &d); err != nil {
			t.Fatalf("sandbox.fallback does not decode: %v\n%s", err, f.Data)
		}
		if !strings.Contains(d.Why, "start Docker") || d.Role != "qa" || d.Wanted != "docker" {
			t.Fatalf("sandbox.fallback decoded as %+v from %s", d, f.Data)
		}

		return
	}

	t.Fatal("no sandbox.fallback fact was reported")
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
	r := newPipeRig(t, rigOpts{limits: Limits{GroupRetries: 4}})
	r.model.on("builder", building(r.wt, "a.go", "package a\n", builderDone), sayResult(builderDone))
	r.model.on("qa", sayResult(qaPass), sayResult(qaPass))
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

func TestAFailureWithNoRetriesLeftEscalatesRetries(t *testing.T) {
	r := newPipeRig(t, rigOpts{limits: Limits{GroupRetries: 2}})
	r.model.on(
		"builder",
		building(r.wt, "a.go", "package a\n", builderDone),
		building(r.wt, "b.go", "package b\n", builderDone),
	)
	r.model.on("qa", sayResult(qaPass), sayResult(qaPass))
	r.model.on("reviewer", sayResult(reviewerNot), sayResult(reviewerNot))
	if err := r.run(); err != ErrHeld {
		t.Fatalf("Run returned %v", err)
	}
	if escs := r.rt.escalations(); len(escs) != 1 || escs[0].Reason != "retries" {
		t.Fatalf("escalations %+v", escs)
	}
}

func TestRegressionWithNoRetriesLeftEscalatesRetries(t *testing.T) {
	r := newPipeRig(t, rigOpts{limits: Limits{GroupRetries: 2}})
	r.checks.fail, r.checks.regress = []bool{true, true}, true
	r.model.on(
		"builder",
		building(r.wt, "a.go", "package a\n", builderDone),
		building(r.wt, "b.go", "package b\n", builderDone),
	)
	if err := r.run(); err != ErrHeld {
		t.Fatalf("Run returned %v", err)
	}
	if escs := r.rt.escalations(); len(escs) != 1 || escs[0].Reason != "retries" || escs[0].Kind != EscBlocked {
		t.Fatalf("escalations %+v", escs)
	}
}

func TestCheckDoneFactMatchesTheEventSchemaAndResultFailFlagsARegression(t *testing.T) {
	r := newPipeRig(t, rigOpts{limits: Limits{GroupRetries: 3}})
	r.checks.fail, r.checks.regress = []bool{true, true, true}, true
	r.model.on(
		"builder",
		building(r.wt, "a.go", "package a\n", builderDone),
		building(r.wt, "b.go", "package b\n", builderDone),
		building(r.wt, "c.go", "package c\n", builderDone),
	)
	if err := r.run(); err != ErrHeld {
		t.Fatalf("Run returned %v", err)
	}

	r.rt.mu.Lock()
	facts := slices.Clone(r.rt.facts)
	r.rt.mu.Unlock()

	checks, fails := 0, 0
	for _, f := range facts {
		switch f.Subtype {
		case "check.done":
			checks++
			line := `{"type":"run-status","subtype":"check.done","run":"r","group":"g",` +
				`"time":"2026-10-15T12:00:00Z","data":` + string(f.Data) + `}`

			var e bus.Event
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				t.Fatalf("check.done does not decode as the event: %v\n%s", err, f.Data)
			}
		case "result.fail":
			fails++

			var d struct {
				Regression bool `json:"regression"`
			}
			_ = json.Unmarshal(f.Data, &d)
			if !d.Regression {
				t.Fatalf("result.fail lacks the regression flag: %s", f.Data)
			}
		}
	}
	if checks != 3 || fails != 2 {
		t.Fatalf("check.done %d result.fail %d", checks, fails)
	}
}

func TestDisputedQAResultRestartsTheQAOnceAndASecondEscalatesFlaky(t *testing.T) {
	items := []agent.Item{{ID: "a1", Text: "it works", Cmd: "test"}}
	failed := `{"items":[{"item":"a1","pass":false,"evidence":"it failed"}],"tasks":[{"task":"t1",` +
		`"confirmation":"confirmed"}]}`
	passed := `{"items":[{"item":"a1","pass":true,"evidence":"ok"}],"tasks":[{"task":"t1","confirmation":"confirmed"}]}`

	r := newPipeRig(t, rigOpts{items: items})
	r.model.on("builder", building(r.wt, "a.go", "package a\n", builderDone))
	r.model.on("qa", sayResult(failed), sayResult(passed))
	r.model.on("reviewer", sayResult(reviewerMet))
	r.reachCleanup()
	if r.model.started("qa") != 2 || r.model.started("builder") != 1 || r.rt.progress().Disputes != 1 {
		t.Fatalf("sessions %v, progress %+v", r.model.starts, r.rt.progress())
	}
	if got := r.slot("qa", 1, "checks"); !strings.Contains(got, "Go reran item a1") {
		t.Fatalf("the restart brief lacks Go's output: %q", got)
	}

	f := newPipeRig(t, rigOpts{items: items})
	f.model.on("builder", building(f.wt, "a.go", "package a\n", builderDone))
	f.model.on("qa", sayResult(failed), sayResult(failed))
	f.model.on("reviewer", sayResult(reviewerMet))
	if err := f.run(); err != ErrHeld {
		t.Fatalf("Run returned %v", err)
	}
	if escs := f.rt.escalations(); len(escs) != 1 || escs[0].Reason != "qa" {
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
	r.model.on("qa", sayResult(qaPass), sayResult(qaPass))
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

func TestBuilderSessionsEndTheGroupWhenTheRetriesAreSpent(t *testing.T) {
	r := newPipeRig(t, rigOpts{
		limits: Limits{GroupRetries: 2},
		mut:    func(s *GroupSpec, _ *Collaborators) { s.Tasks[0].DoneWhen = "fail" },
	})
	r.model.on("builder", sayResult(builderDone), sayResult(builderDone))
	if err := r.run(); err != ErrHeld {
		t.Fatalf("Run returned %v", err)
	}
	if escs := r.rt.escalations(); len(escs) != 1 || escs[0].Reason != "retries" {
		t.Fatalf("escalations %+v", escs)
	}
	if r.rt.progress().RetriesLeft != 0 || r.cm.saveCount() != 0 {
		t.Fatalf("progress %+v", r.rt.progress())
	}
}

func TestUnfinishedBuilderSessionReportsTheChangedFiles(t *testing.T) {
	r := newPipeRig(t, rigOpts{limits: Limits{GroupRetries: -1}})
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

func TestSessionEndFactCarriesTheReportedTokens(t *testing.T) {
	r := newPipeRig(t, rigOpts{limits: Limits{GroupRetries: -1}})
	r.model.on("builder", func(llm.Session) (llm.Stream, error) {
		st := newFakeStream()
		ev := resultEvent(`{"items":[]}`)
		ev.Result.Usage = &llm.Usage{TokensIn: 30, TokensOut: 9}
		st.ev <- ev
		return st, nil
	})
	if err := r.run(); err != ErrHeld {
		t.Fatalf("Run returned %v", err)
	}
	if r.rt.factCount("usage") != 1 {
		t.Fatalf("%d usage facts, want 1", r.rt.factCount("usage"))
	}
	r.rt.mu.Lock()
	defer r.rt.mu.Unlock()
	for _, f := range r.rt.facts {
		if f.Subtype != "session.end" {
			continue
		}
		var d struct {
			Tokens int64 `json:"tokens"`
			Usage  *struct {
				In  int `json:"tokens_in"`
				Out int `json:"tokens_out"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(f.Data, &d); err != nil {
			t.Fatal(err)
		}
		if d.Tokens != 39 || d.Usage == nil || d.Usage.In != 30 || d.Usage.Out != 9 {
			t.Fatalf("session.end carries %s, want tokens 39 and usage 30 in, 9 out", f.Data)
		}
		return
	}
	t.Fatal("no session.end fact")
}
