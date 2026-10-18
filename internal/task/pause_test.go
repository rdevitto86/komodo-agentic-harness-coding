package task

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
)

// wake is the Runtime Manager's timer wake-up.
var wake = Wake{Reason: "timer"}

func TestGroupStartedWithEveryLinkLimitedPausesAndMakesNoModelCall(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	r.lim.resets[llm.AnthropicAPI] = t0.Add(time.Hour)
	if err := r.run(); !errors.Is(err, ErrGroupPaused) {
		t.Fatalf("Run returned %v", err)
	}
	p := r.rt.progress().Pause
	if p == nil || p.Since.IsZero() || !p.WaitUntil.Equal(t0.Add(time.Hour+time.Minute)) ||
		p.WaitLink != string(llm.AnthropicAPI) {
		t.Fatalf("pause %+v", p)
	}
	if r.model.total() != 0 || r.m.Progress().Attempts != 0 {
		t.Fatalf("model calls %d, attempts %d", r.model.total(), r.m.Progress().Attempts)
	}
	if err := r.m.Stop(context.Background(), "pause_timeout"); err != nil {
		t.Fatal(err)
	}
	if escs := r.rt.escalations(); len(escs) != 1 || escs[0].Kind != EscBlocked || escs[0].Reason != "pause_timeout" {
		t.Fatalf("escalations %+v", escs)
	}
}

func TestEveryLinkAtItsSlotLimitPausesTheGroup(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	r.lim.full[llm.AnthropicAPI] = true
	if err := r.run(); !errors.Is(err, ErrGroupPaused) {
		t.Fatalf("Run returned %v", err)
	}
	if r.model.total() != 0 || r.rt.progress().Pause == nil {
		t.Fatalf("model calls %d, pause %+v", r.model.total(), r.rt.progress().Pause)
	}
}

func TestProviderLimitMidSessionPausesAndRefusedResumeKeepsSinceAndCountsTheRefusal(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	r.model.on("builder", limitStep(t0.Add(time.Hour)))
	if err := r.run(); !errors.Is(err, ErrGroupPaused) {
		t.Fatalf("Run returned %v", err)
	}
	first := r.rt.progress().Pause
	if first == nil || len(first.Owed) != 1 || first.Owed[0] != "builder" || r.rt.progress().Attempts != 0 {
		t.Fatalf("pause %+v, progress %+v", first, r.rt.progress())
	}
	if _, ok := r.rt.progress().Handles["builder"]; !ok {
		t.Fatal("the builder's handle was not kept for the resume")
	}
	delete(r.lim.resets, llm.AnthropicAPI)
	r.model.resume = func(llm.Session, llm.Input) (llm.Stream, error) {
		return nil, &llm.ProviderLimit{Link: llm.AnthropicAPI, Cap: llm.CapWindow}
	}
	if err := r.within(func() error { return r.m.Resume(context.Background(), wake) }); !errors.Is(err, ErrGroupPaused) {
		t.Fatalf("Resume returned %v", err)
	}
	again := r.rt.progress().Pause
	if again == nil || !again.Since.Equal(first.Since) || again.RefusedResumes != 1 {
		t.Fatalf("re-pause %+v, first %+v", again, first)
	}
	if r.model.started("builder") != 1 || r.rt.progress().Attempts != 0 {
		t.Fatalf("sessions %v, attempts %d", r.model.starts, r.rt.progress().Attempts)
	}
}

func TestResumeContinuesTheSavedSessionAndChargesNoAttempt(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	r.model.on("builder", limitStep(t0.Add(time.Hour)))
	if err := r.run(); !errors.Is(err, ErrGroupPaused) {
		t.Fatalf("Run returned %v", err)
	}
	delete(r.lim.resets, llm.AnthropicAPI)
	var in llm.Input
	r.model.resume = func(_ llm.Session, i llm.Input) (llm.Stream, error) {
		in = i
		return building(r.wt, "a.go", "package a\n", builderDone)(llm.Session{})
	}
	r.model.on("tester", sayResult(testerPass))
	r.model.on("reviewer", sayResult(reviewerMet))
	r.reachCleanupWith(func(ctx context.Context) error { return r.m.Resume(ctx, wake) })
	if in.Text != "Continue where you stopped." || r.model.started("builder") != 1 || r.rt.progress().Attempts != 0 {
		t.Fatalf("resume input %q, sessions %v", in.Text, r.model.starts)
	}
}

func TestSavedHandleResumesAndAFailingHandleStartsFreshWithResumeLost(t *testing.T) {
	for _, fails := range []bool{false, true} {
		r := newPipeRig(t, rigOpts{mut: func(s *GroupSpec, c *Collaborators) {
			s.Saved = &Progress{
				Stage: "build",
				Handles: map[string]llm.ResumeHandle{"builder": {Link: llm.AnthropicAPI, Kind: "messages_file",
					Value: filepath.Join(c.SessionsDir, "sid-1", "messages.jsonl")}}}
		}})
		resumed := 0
		r.model.resume = func(llm.Session, llm.Input) (llm.Stream, error) {
			resumed++
			if fails {
				return nil, errors.New("the saved conversation is gone")
			}
			return building(r.wt, "a.go", "package a\n", builderDone)(llm.Session{})
		}
		if fails {
			r.model.on("builder", building(r.wt, "a.go", "package a\n", builderDone))
		}
		r.model.on("tester", sayResult(testerPass))
		r.model.on("reviewer", sayResult(reviewerMet))
		r.reachCleanup()
		wantStarts := 0
		if fails {
			wantStarts = 1
		}
		if resumed != 1 || r.model.started("builder") != wantStarts || (r.rt.factCount("resume_lost") == 1) != fails {
			t.Fatalf(
				"fails=%v: resumed %d, starts %v, resume_lost %d",
				fails,
				resumed,
				r.model.starts,
				r.rt.factCount("resume_lost"),
			)
		}
	}
}

func TestFailoverToTheNextLinkChargesNoAttempt(t *testing.T) {
	r := newPipeRig(t, rigOpts{chain: []string{
		string(llm.ClaudeCLI),
		string(llm.AnthropicAPI),
	}, starts: map[llm.Link]func(llm.Session) (llm.Stream, error){
		llm.ClaudeCLI: func(llm.Session) (llm.Stream, error) {
			return nil, &llm.ProviderLimit{Link: llm.ClaudeCLI, Cap: llm.CapWindow, ResetAt: t0.Add(5 * time.Hour)}
		}}})
	r.model.on("builder", building(r.wt, "a.go", "package a\n", builderDone))
	r.model.on("tester", sayResult(testerPass))
	r.model.on("reviewer", sayResult(reviewerMet))
	r.reachCleanup()
	if r.rt.progress().Attempts != 0 || r.model.started("builder") != 1 {
		t.Fatalf("attempts %d, sessions %v", r.rt.progress().Attempts, r.model.starts)
	}
	if at, ok := r.lim.resets[llm.ClaudeCLI]; !ok || !at.Equal(t0.Add(5*time.Hour)) {
		t.Fatalf("the limit was not recorded: %v", r.lim.resets)
	}
}

func TestPauseCommandLetsTheSessionFinishThenHoldsUntilWake(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	release, started := make(chan struct{}), make(chan struct{})
	r.model.on("builder", func(s llm.Session) (llm.Stream, error) {
		close(started)
		<-release
		return building(r.wt, "a.go", "package a\n", builderDone)(s)
	})
	done := make(chan error, 1)
	go func() { done <- r.m.Run(context.Background()) }()
	<-started
	until := time.Now().Add(3 * time.Hour)
	if err := r.m.Pause(context.Background(), "claude-cli", until); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, ErrGroupPaused) {
		t.Fatalf("Run returned %v", err)
	}
	p := r.rt.progress().Pause
	if p == nil || !p.WaitUntil.Equal(until) || r.model.started("tester") != 0 || r.cm.saveCount() != 1 {
		t.Fatalf("pause %+v, sessions %v", p, r.model.starts)
	}
	r.model.on("tester", sayResult(testerPass))
	r.model.on("reviewer", sayResult(reviewerMet))
	r.reachCleanupWith(func(ctx context.Context) error { return r.m.Resume(ctx, wake) })
	if r.rt.progress().Pause != nil {
		t.Fatal("the pause was not released")
	}
}

func TestTesterLimitedMidRoundIsOwedAtTheTimerAndTheReviewerIsNotRerun(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	r.model.on("builder", building(r.wt, "a.go", "package a\n", builderDone))
	r.model.on("tester", limitStep(t0.Add(time.Hour)), sayResult(testerPass))
	r.model.on("reviewer", sayResult(reviewerMet))
	if err := r.run(); !errors.Is(err, ErrGroupPaused) {
		t.Fatalf("Run returned %v", err)
	}
	p := r.rt.progress().Pause
	if p == nil || len(p.Owed) != 1 || p.Owed[0] != "tester" || r.model.started("tester") != 1 {
		t.Fatalf("pause %+v, sessions %v", p, r.model.starts)
	}
	delete(r.lim.resets, llm.AnthropicAPI)
	r.model.resume = func(llm.Session, llm.Input) (llm.Stream, error) { return nil, llm.ErrUnavailable }
	r.reachCleanupWith(func(ctx context.Context) error { return r.m.Resume(ctx, wake) })
	if r.model.started("reviewer") != 1 || r.checks.calls != 1 || len(r.cm.published()) != 1 {
		t.Fatalf("sessions %v, checks %d", r.model.starts, r.checks.calls)
	}
}

func TestStopCancelsTheLiveSessionAndRunReturnsNil(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	started := make(chan struct{})
	var st *fakeStream
	r.model.on("builder", func(llm.Session) (llm.Stream, error) {
		st = newFakeStream()
		st.onStop = func(f *fakeStream, _ llm.StopLevel) { f.errs <- context.Canceled }
		close(started)
		return st, nil
	})
	done := make(chan error, 1)
	go func() { done <- r.m.Run(context.Background()) }()
	<-started
	if err := r.m.Stop(context.Background(), "stop"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}
	if got := st.stopped(); len(got) == 0 || got[0] != llm.Interrupt {
		t.Fatalf("the session was not interrupted: %v", got)
	}
	if err := r.m.Stop(context.Background(), "stop"); err != nil {
		t.Fatal("a second Stop failed")
	}
}

func TestTokenCapNudgeReachesEachLiveSessionWithItsRoleLine(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	started := make(chan struct{})
	var st *fakeStream
	r.model.on("builder", func(llm.Session) (llm.Stream, error) {
		st = newFakeStream()
		st.onStop = func(f *fakeStream, _ llm.StopLevel) { f.errs <- context.Canceled }
		close(started)
		return st, nil
	})
	done := make(chan error, 1)
	go func() { done <- r.m.Run(context.Background()) }()
	<-started
	for len(st.injected()) == 0 {
		r.m.Nudge()
		time.Sleep(5 * time.Millisecond)
	}
	if got := st.injected()[0]; !strings.Contains(got, "wrap up your current task and save it") {
		t.Fatalf("builder line %q", got)
	}
	_ = r.m.Stop(context.Background(), "stop")
	<-done
}

func TestRecheckReportsTheChecksOfTheEpicTip(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	r.checks.fail = []bool{true, false}
	var got []string
	r.m.c.OnRecheck = func(pass bool, out string) {
		if pass {
			got = append(got, "pass")
		} else {
			got = append(got, "fail:"+out)
		}
	}
	r.m.Recheck(context.Background())
	r.m.Recheck(context.Background())
	if len(got) != 2 || !strings.HasPrefix(got[0], "fail:") || !strings.Contains(got[0], "FAIL: exact output") ||
		got[1] != "pass" {
		t.Fatalf("recheck results %v", got)
	}
	if _, err := os.Stat(r.wt); err != nil {
		t.Fatal(err)
	}
}

// limitStep is a step whose stream ends at once with a provider limit that lifts at reset.
func limitStep(reset time.Time) func(llm.Session) (llm.Stream, error) {
	return func(llm.Session) (llm.Stream, error) {
		st := newFakeStream()
		st.errs <- &llm.ProviderLimit{Link: llm.AnthropicAPI, Cap: llm.CapWindow, ResetAt: reset}
		return st, nil
	}
}
