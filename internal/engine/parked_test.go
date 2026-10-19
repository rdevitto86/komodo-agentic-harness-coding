package engine

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// parkedEng returns an engine whose store holds a group saved waiting for an override until the given time,
// with no run live.
func parkedEng(t *testing.T, until time.Time) *eng {
	t.Helper()
	x := newEng(t, [][]string{{"tg-1"}}, onlyIdle, withClock)
	x.savedGroup("tg-1", StageBlocked, func(s *State) {
		s.Prior, s.Counters.RetriesLeft = StageTestReview, 0
		s.Block = state.BlockState{Reason: state.ReasonRetries, At: t0, Until: until, Counter: "test_review"}
	})
	x.e.m.d.Parked = func(context.Context) []ParkedGroup {
		return []ParkedGroup{{Repo: "github.com/acme/shop", Root: x.root, CommonDir: x.root + "/.git", Group: "g-tg-1"}}
	}
	return x
}

func TestAStartRearmsTheSavedOverrideTimerAndTheDeadlineLapsesTheGroup(t *testing.T) {
	x := parkedEng(t, t0.Add(30*time.Minute))
	x.serve()

	until(t, "the timer to be armed", func() bool { return x.e.m.Live() == 1 })
	if st := x.store.get("g-tg-1"); st.Stage != StageBlocked {
		t.Fatalf("the group moved before its deadline: %s", st.Stage)
	}

	x.clock.Advance(29 * time.Minute)
	if st := x.store.get("g-tg-1"); st.Stage != StageBlocked {
		t.Fatalf("the group lapsed early: %s", st.Stage)
	}

	x.clock.Advance(2 * time.Minute)
	until(t, "the group to fail", func() bool { return x.store.get("g-tg-1").Stage == StageDone })
	if st := x.store.get("g-tg-1"); st.Outcome != "failed" {
		t.Fatalf("outcome %q", st.Outcome)
	}
	until(t, "the lapse events", func() bool {
		return x.ev.count(bus.TypeRunStatus, "override.lapsed") == 1 && x.ev.count(bus.TypeRunFailure, "group.failed") == 1
	})
}

func TestADeadlineThatPassedWhileTheEngineWasDownLapsesAtOnce(t *testing.T) {
	x := parkedEng(t, t0.Add(-time.Hour))
	x.serve()

	until(t, "the group to fail at start", func() bool { return x.store.get("g-tg-1").Stage == StageDone })
	if st := x.store.get("g-tg-1"); st.Outcome != "failed" {
		t.Fatalf("outcome %q", st.Outcome)
	}
	until(t, "the lapse events", func() bool {
		return x.ev.count(bus.TypeRunStatus, "override.lapsed") == 1 && x.ev.count(bus.TypeRunFailure, "group.failed") == 1
	})
}

// statusOf asks the engine for its status.
func statusOf(x *eng) StatusData {
	x.t.Helper()
	reps := x.ask(Request{ID: "st", Verb: VerbStatus, Root: x.root})
	var sd StatusData
	if err := json.Unmarshal(reps[len(reps)-1].Data, &sd); err != nil {
		x.t.Fatal(err)
	}
	return sd
}

func TestAParkedGroupIsListedInStatusAsBlockedOnRetries(t *testing.T) {
	x := parkedEng(t, t0.Add(30*time.Minute))
	x.e.m.d.Parked = func(context.Context) []ParkedGroup {
		return []ParkedGroup{{
			Repo: "github.com/acme/shop", Root: x.root, CommonDir: x.root + "/.git", Group: "g-tg-1", Label: "tg-1",
		}}
	}
	x.serve()
	until(t, "the timer to be armed", func() bool { return x.e.m.Live() == 1 })

	sd := statusOf(x)
	if len(sd.Blocked) != 1 || sd.Blocked[0].Group != "tg-1" || sd.Blocked[0].Reason != string(state.ReasonRetries) ||
		sd.Blocked[0].RunID != "old-run" || sd.Blocked[0].Question == "" {
		t.Fatalf("blocked %+v", sd.Blocked)
	}
}

func TestAnOverrideAfterARestartLoadsTheParkedGroupAndSendsItToTheBuilder(t *testing.T) {
	x := parkedEng(t, t0.Add(30*time.Minute))
	x.e.peer = func(net.Conn) (bool, error) { return true, nil }
	x.serve()
	until(t, "the timer to be armed", func() bool { return x.e.m.Live() == 1 })

	reps := x.ask(overrideReq("o1"))
	if last := reps[len(reps)-1]; last.Status != StatusOK {
		t.Fatalf("reply %+v", last)
	}
	x.waitStage("tg-1", StageBuild)
	if st := x.store.get("g-tg-1"); st.Counters.Overrides != 1 {
		t.Fatalf("overrides %d", st.Counters.Overrides)
	}
	if got := x.ev.count(bus.TypeRunStatus, "request.refused"); got != 0 {
		t.Fatalf("the load refused %d requests", got)
	}

	x.clock.Advance(time.Hour)
	if st := x.store.get("g-tg-1"); st.Stage == StageDone {
		t.Fatal("the old override timer still lapsed the group")
	}
}

// parkedPair returns an engine whose store holds two groups of one saved run, both waiting for an override.
func parkedPair(t *testing.T) *eng {
	t.Helper()
	x := newEng(t, [][]string{{"tg-1"}, {"tg-2"}}, onlyIdle, withClock)

	var rows []ParkedGroup

	for _, label := range []string{"tg-1", "tg-2"} {
		x.savedGroup(label, StageBlocked, func(s *State) {
			s.Prior, s.Counters.RetriesLeft = StageTestReview, 0
			s.Block = state.BlockState{
				Reason: state.ReasonRetries, At: t0, Until: t0.Add(30 * time.Minute), Counter: "test_review",
			}
		})
		rows = append(rows, ParkedGroup{
			Repo: "github.com/acme/shop", Root: x.root, CommonDir: x.root + "/.git", Group: "g-" + label, Label: label,
		})
	}

	x.e.m.d.Parked = func(context.Context) []ParkedGroup { return rows }
	x.e.peer = func(net.Conn) (bool, error) { return true, nil }

	return x
}

func TestTwoParkedGroupsOfOneRunCanEachBeOverriddenAfterARestart(t *testing.T) {
	x := parkedPair(t)
	x.serve()
	until(t, "both timers to be armed", func() bool { return x.e.m.Live() == 2 })

	for _, label := range []string{"tg-1", "tg-2"} {
		req := overrideReq("o-" + label)
		req.Target = label
		req.Event.Data = &bus.RunContinue{Target: label, Answer: "override", Minutes: 60, Request: bus.ID(req.ID)}

		reps := x.ask(req)
		if last := reps[len(reps)-1]; last.Status != StatusOK {
			t.Fatalf("%s: reply %+v", label, last)
		}
		x.waitStage(label, StageBuild)
	}
}

func TestTwoOverridesAtOnceOnOneParkedGroupAcceptOneAndRefuseTheOtherAsUsed(t *testing.T) {
	x := parkedPair(t)
	x.serve()
	until(t, "both timers to be armed", func() bool { return x.e.m.Live() == 2 })

	errs := make(chan error, 2)

	for i := range 2 {
		go func() {
			errs <- x.e.m.Override(context.Background(), "tg-1", "ov-"+string(rune('a'+i)), 30)
		}()
	}

	var ok, refused int

	for range 2 {
		switch err := <-errs; {
		case err == nil:
			ok++
		case strings.Contains(err.Error(), "already used") && !strings.Contains(err.Error(), "no live group"):
			refused++
		default:
			t.Errorf("unexpected refusal: %v", err)
		}
	}
	if ok != 1 || refused != 1 {
		t.Fatalf("accepted %d, refused as used %d", ok, refused)
	}
	if st := x.store.get("g-tg-1"); st.Counters.Overrides != 1 {
		t.Fatalf("overrides %d", st.Counters.Overrides)
	}
}
