package engine

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// errRig is an error handler over a fake clock that records its events and the groups it resumed.
type errRig struct {
	h       *errorHandler
	clk     *FakeClock
	mu      sync.Mutex
	events  []string
	resumed []string
}

func newErrRig() *errRig {
	r := &errRig{clk: NewFakeClock(t0)}
	r.h = newErrorHandler(
		r.clk,
		func(e Event) {
			r.mu.Lock()
			r.events = append(r.events, string(e.Subtype))
			r.mu.Unlock()
		},
		func(g string) {
			r.mu.Lock()
			r.resumed = append(r.resumed, g)
			r.mu.Unlock()
		},
	)
	return r
}

// log returns copies of the events that are not error.recorded, and the resumed groups.
func (r *errRig) log() (events, resumed []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	events = slices.DeleteFunc(slices.Clone(r.events), func(s string) bool { return s == "error.recorded" })
	return events, slices.Clone(r.resumed)
}

// hit records one error of a reason on each named group.
func (r *errRig) hit(reason state.Reason, groups ...string) {
	for _, g := range groups {
		r.h.record("run", g, reason, 1)
	}
}

func TestThreeGroupsWithOneErrorTypePauseEveryStart(t *testing.T) {
	r := newErrRig()
	r.hit(state.ReasonCrash, "g1", "g2")

	if r.h.startsHeld() {
		t.Fatal("two groups paused the starts")
	}

	r.hit(state.ReasonCrash, "g3")

	if ev, _ := r.log(); !r.h.startsHeld() || !slices.Equal(ev, []string{"starts.paused"}) {
		t.Fatalf("held %v events %v", r.h.startsHeld(), ev)
	}
	if r.h.allow("g5") || r.h.alert() == "" {
		t.Fatal("a pause let a start through or showed no alert")
	}
}

func TestDifferentTypesOneGroupAndOldErrorsNeverPause(t *testing.T) {
	r := newErrRig()
	for range 5 {
		r.hit(state.ReasonSetup, "g1")
	}

	r.hit(state.ReasonContract, "g2")
	r.hit(state.ReasonStateInvalid, "g3")

	if r.h.startsHeld() {
		t.Fatal("one group, or three different types, paused the starts")
	}

	r = newErrRig()
	r.hit(state.ReasonSetup, "g1", "g2")
	r.clk.Advance(pauseWindow + time.Second)
	r.hit(state.ReasonSetup, "g3")

	if r.h.startsHeld() {
		t.Fatal("errors more than ten minutes apart paused the starts")
	}
}

func TestWorkOutcomesAndMinorErrorsNeverPause(t *testing.T) {
	r := newErrRig()
	for _, reason := range []state.Reason{
		state.ReasonCheckFail, state.ReasonTimeout, state.ReasonProvider, state.ReasonCancelled, state.ReasonPublish,
	} {
		r.hit(reason, "g1", "g2", "g3", "g4")
	}

	if r.h.startsHeld() {
		t.Fatal("a work outcome, a rate limit or a restart paused the starts")
	}
}

func TestThePauseWaitsOneThenTwoThenFourMinutesAndThenStaysPausedForAPerson(t *testing.T) {
	r := newErrRig()
	r.hit(state.ReasonSetup, "g1", "g2", "g3")
	r.h.allow("g1")

	for _, wait := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute} {
		r.clk.Advance(wait - time.Second)
		if !r.h.startsHeld() {
			t.Fatalf("starts opened before the wait of %s ended", wait)
		}

		r.clk.Advance(time.Second)
		if r.h.startsHeld() {
			t.Fatalf("starts stayed held after the wait of %s", wait)
		}

		r.hit(state.ReasonSetup, "g1", "g2", "g3")
	}

	r.clk.Advance(time.Hour)
	if !r.h.startsHeld() || r.h.alert() == "" {
		t.Fatal("after the 4-minute wait failed the pause did not stay")
	}

	r.h.person()
	if r.h.startsHeld() || r.h.alert() != "" {
		t.Fatal("a person did not end the pause")
	}
}

func TestAHeldGroupResumesWhenTheWaitEnds(t *testing.T) {
	r := newErrRig()
	r.hit(state.ReasonSetup, "g1", "g2", "g3")

	if r.h.allow("g9") {
		t.Fatal("a start went through the pause")
	}

	r.clk.Advance(time.Minute)
	ev, resumed := r.log()
	if !slices.Equal(ev, []string{"starts.paused", "starts.resumed"}) || !slices.Equal(resumed, []string{"g9"}) {
		t.Fatalf("events %v resumed %v", ev, resumed)
	}
}

func TestAnEngineFailureStopsStartsAndNeverAutoResumes(t *testing.T) {
	r := newErrRig()
	r.h.engineFailed("goroutine", errors.New("boom"))

	if r.h.allow("g1") || r.h.alert() == "" {
		t.Fatal("a start went through after an engine failure")
	}

	r.clk.Advance(24 * time.Hour)
	if r.h.allow("g1") {
		t.Fatal("the engine resumed by itself")
	}

	if ev, _ := r.log(); !slices.Equal(ev, []string{"engine.failed"}) {
		t.Fatalf("events %v", ev)
	}

	r.h.person()
	if _, resumed := r.log(); !r.h.allow("g1") || !slices.Equal(resumed, []string{"g1"}) {
		t.Fatalf("a person's run did not resume the starts: %v", resumed)
	}
}

func TestTheHandlerCountsEveryErrorByTypeWithItsTier(t *testing.T) {
	r := newErrRig()
	r.hit(state.ReasonCrash, "g1", "g2")
	r.hit(state.ReasonTimeout, "g1")
	r.h.engineFailed("state store", errors.New("gone"))

	got := r.h.counted()
	want := []ErrorCount{
		{Type: "crash", Tier: "blocker", Count: 2},
		{Type: "engine", Tier: "severe", Count: 1},
		{Type: "timeout", Tier: "work", Count: 1},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("counts %v, want %v", got, want)
	}
}

func TestOnlyABlockerReturnsAGroupWait(t *testing.T) {
	r := newErrRig()
	if got := r.h.record("run", "g1", state.ReasonCrash, 2); got != 2*time.Minute {
		t.Errorf("crash wait %s", got)
	}

	for _, reason := range []state.Reason{state.ReasonCheckFail, state.ReasonProvider, state.ReasonPublish} {
		if got := r.h.record("run", "g1", reason, 2); got != 0 {
			t.Errorf("%s waited %s", reason, got)
		}
	}
}
