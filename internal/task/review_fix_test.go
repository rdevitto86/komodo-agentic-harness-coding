package task

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// ctxBriefs blocks Quality Assurance's brief until its context ends, as a slow Context Manager would.
type ctxBriefs struct {
	inner Briefer
	n     *atomic.Int32
}

func (b ctxBriefs) Brief(ctx context.Context, name string, slots map[string]string) (Brief, error) {
	if name == "qa" && b.n.Add(1) == 1 {
		<-ctx.Done()
		return Brief{}, ctx.Err()
	}
	return b.inner.Brief(ctx, name, slots)
}

// panicRole panics when its brief values are asked for.
type panicRole struct{ Role }

func (panicRole) Slots(context.Context, RoleInput) (map[string]string, error) { panic("boom") }

// blockingChecks blocks until its context ends.
type blockingChecks struct{ started chan struct{} }

func (b blockingChecks) Check(ctx context.Context, _ agent.CommandRunner, _ agent.CheckInput) (agent.CheckResult, error) {
	close(b.started)
	<-ctx.Done()
	return agent.CheckResult{}, ctx.Err()
}

func TestBuilderPauseDoesNotSkipTheChecksOfTestReview(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	r.model.on("builder", limitStep(t0.Add(time.Hour)))
	if err := r.run(); !errors.Is(err, ErrGroupPaused) {
		t.Fatalf("Run returned %v", err)
	}
	delete(r.lim.resets, llm.AnthropicAPI)
	r.model.resume = func(llm.Session, llm.Input) (llm.Stream, error) {
		return building(r.wt, "a.go", "package a\n", builderDone)(llm.Session{})
	}
	r.model.on("qa", sayResult(qaPass))
	r.model.on("reviewer", sayResult(reviewerMet))
	r.reachCleanupWith(func(ctx context.Context) error { return r.m.Resume(ctx, wake) })
	if r.checks.calls != 1 {
		t.Fatalf("the free checks ran %d times after a builder pause, want 1", r.checks.calls)
	}
}

func TestPauseRecordedBeforeRunHoldsTheGroup(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	if err := r.m.Pause(context.Background(), "claude-cli", time.Now().Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := r.run(); !errors.Is(err, ErrGroupPaused) {
		t.Fatalf("Run returned %v", err)
	}
	if r.model.total() != 0 || r.m.Progress().Pause == nil {
		t.Fatalf("%d sessions ran while paused", r.model.total())
	}
}

func TestReviewerReturnWhileTheQABriefsDoesNotFailTheGroup(t *testing.T) {
	r := newPipeRig(t, rigOpts{
		mut: func(_ *GroupSpec, c *Collaborators) { c.Briefs = ctxBriefs{c.Briefs, new(atomic.Int32)} },
	})
	r.model.on(
		"builder",
		building(r.wt, "a.go", "package a\n", builderDone),
		building(r.wt, "a.go", "package a\n// fixed\n", builderDone),
	)
	r.model.on("reviewer", sayResult(reviewerNot), sayResult(reviewerMet))
	r.model.on("qa", sayResult(qaPass))
	r.reachCleanup()
	if r.rt.progress().Returns != 1 {
		t.Fatalf("progress %+v", r.rt.progress())
	}
}

func TestPanickingJudgeNeverPublishes(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	r.m.c.Roles["reviewer"] = panicRole{r.m.c.Roles["reviewer"]}
	r.model.on("builder", building(r.wt, "a.go", "package a\n", builderDone))
	r.model.on("qa", sayResult(qaPass))
	err := r.run()
	if err == nil || len(r.cm.published()) != 0 {
		t.Fatalf("Run returned %v, published %d", err, len(r.cm.published()))
	}
}

func TestPanicInAServedCallEndsItAndAStopStillReturns(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	r.m.c.Roles["builder"] = panicRole{r.m.c.Roles["builder"]}
	r.m.rt = nil
	cmds, replies := make(chan Command, 4), make(chan Message, 64)
	done := make(chan error, 1)
	go func() { done <- r.m.Serve(context.Background(), Channels{Commands: cmds, Replies: replies}) }()
	cmds <- Command{Kind: CmdStart}
	var ended *Ended
	for ended == nil {
		select {
		case msg := <-replies:
			if msg.Kind == MsgEnded {
				ended = msg.Ended
			}
		case <-time.After(3 * time.Second):
			t.Fatal("the panicked call never ended")
		}
	}
	if ended.Result != "failed" {
		t.Fatalf("ended %+v", ended)
	}
	cmds <- Command{Kind: CmdStop, Cause: "user"}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Serve hung after a stop")
	}
	if r.lim.slots[llm.AnthropicAPI] != 0 {
		t.Fatalf("a link slot leaked: %v", r.lim.slots)
	}
}

func TestStopCancelsARunningRecheckBeforeServeWaitsForIt(t *testing.T) {
	started := make(chan struct{})
	r := newPipeRig(t, rigOpts{mut: func(_ *GroupSpec, c *Collaborators) {
		c.Checks = blockingChecks{started}
		c.OnRecheck = func(bool, string) {}
	}})
	r.m.rt = nil
	cmds, replies := make(chan Command, 4), make(chan Message, 64)
	done := make(chan error, 1)
	go func() { done <- r.m.Serve(context.Background(), Channels{Commands: cmds, Replies: replies}) }()
	cmds <- Command{Kind: CmdRecheck}
	<-started
	cmds <- Command{Kind: CmdStop, Cause: "stop"}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Serve waited on a recheck that nothing cancelled")
	}
}

func TestLostResumeStartsFreshWithTheBriefAndTheInputWritten(t *testing.T) {
	r := newPipeRig(t, rigOpts{mut: func(s *GroupSpec, c *Collaborators) {
		s.Saved = &Progress{
			Stage: state.StageBuild,
			Handles: map[string]llm.ResumeHandle{"builder": {Link: llm.AnthropicAPI, Kind: "messages_file",
				Value: filepath.Join(c.SessionsDir, "sid-1", "messages.jsonl")}}}
	}})
	r.model.resume = func(llm.Session, llm.Input) (llm.Stream, error) { return nil, errors.New("gone") }
	var fresh llm.Session
	r.model.on("builder", func(s llm.Session) (llm.Stream, error) {
		fresh = s
		return building(r.wt, "a.go", "package a\n", builderDone)(s)
	})
	r.model.on("qa", sayResult(qaPass))
	r.model.on("reviewer", sayResult(reviewerMet))
	r.reachCleanup()
	for _, f := range []string{"brief.md", "system.md"} {
		if _, err := os.Stat(filepath.Join(fresh.Dir, f)); err != nil {
			t.Fatalf("the fresh session folder lacks %s: %v", f, err)
		}
	}
	b, _ := os.ReadFile(filepath.Join(fresh.Dir, "brief.md"))
	if !strings.Contains(string(b), "Continue where you stopped.") ||
		!strings.Contains(fresh.Parts[4], "Continue where you stopped.") {
		t.Fatalf("the fresh brief dropped the resume input: %q", b)
	}
}

func TestScopeAnswerReachesOnlyTheNextBrief(t *testing.T) {
	rv := testRoles(t)["builder"]
	rv.(*builderRole).SetScopeAnswer("scope request denied")
	first, _ := rv.Slots(context.Background(), RoleInput{})
	second, _ := rv.Slots(context.Background(), RoleInput{})
	if first["scope_answer"] != "scope request denied" || second["scope_answer"] != "" {
		t.Fatalf("scope answers: %q then %q", first["scope_answer"], second["scope_answer"])
	}
}

func TestQASummaryListsUnconfirmedAndSkippedItems(t *testing.T) {
	roles, err := NewRoles(RoleConfig{Group: "tg-1", Tasks: []TaskSpec{
		{ID: "t1", Title: "x", DoneWhen: "build"},
	}, Worktree: t.TempDir(),
		Items: []agent.Item{
			{ID: "a1", Text: "looks right"},
			{ID: "a2", Text: "reaches the net", Cmd: "test", Needs: []string{"network"}},
		},
		Files: func() []string { return []string{"*.go"} },
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := roles["qa"].Judge(
		context.Background(),
		result(
			`{"status":"tested","items":[`+
				`{"item":"a1","pass":true,"confidence":90,"evidence":[{"cmd":"read","exit":0,"excerpt":"ok"}]},`+
				`{"item":"a2","pass":false,"confidence":60,"evidence":[{"cmd":"test","exit":1,"excerpt":"no"}]}],`+
				`"tasks":[{"task":"t1","confirmation":"confirmed"}]}`,
		),
	)
	if err != nil || out.Kind != OutcomePass {
		t.Fatalf("outcome %+v, %v", out, err)
	}
	all := strings.Join(out.Summary, "\n")
	if !strings.Contains(all, "skipped") || !strings.Contains(all, "a2") || !strings.Contains(all, "network") ||
		!strings.Contains(all, "a1 90") {
		t.Fatalf("summary %q", all)
	}
}
