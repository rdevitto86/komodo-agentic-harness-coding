package engine

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

// brkRig is a breaker over a fake clock that records its events and the groups it resumed.
type brkRig struct {
	b       *breaker
	clk     *FakeClock
	mu      sync.Mutex
	events  []string
	resumed []string
}

func newBrkRig() *brkRig {
	r := &brkRig{clk: NewFakeClock(t0)}
	r.b = newBreaker(
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

// log returns copies of the recorded events and resumed groups.
func (r *brkRig) log() (events, resumed []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events), slices.Clone(r.resumed)
}

// trip opens the breaker with the same reason on three groups.
func (r *brkRig) trip() {
	for _, g := range []string{"g1", "g2", "g3"} {
		r.b.note(g, state.ReasonSetup)
	}
}

func TestThreePanicFaultsOfOneReasonOnThreeGroupsOpenTheBreaker(t *testing.T) {
	r := newBrkRig()
	r.trip()

	if ev, _ := r.log(); !r.b.isOpen() || !slices.Equal(ev, []string{"breaker.opened"}) {
		t.Fatalf("open %v events %v", r.b.isOpen(), ev)
	}
	if r.b.allow("g4") || r.b.alert() == "" {
		t.Fatal("an open breaker let a session start or showed no alert")
	}
}

func TestFaultAndMinorTiersNeverTripTheBreaker(t *testing.T) {
	r := newBrkRig()
	for range 4 {
		ends := []Input{
			crashEnd("builder"),
			builderEnd("provider"),
			builderEnd("cancelled"),
			in(InFault, FaultData{Reason: "publish"}),
		}
		for _, end := range ends {
			res := Next(claimed(), end, fixtureLimits())
			if res.Panic != "" {
				t.Fatalf("%s was counted as a panic: %q", end.Kind, res.Panic)
			}
			if res.Panic != "" {
				r.b.note("g", res.Panic)
			}
		}
	}
	if r.b.isOpen() {
		t.Fatal("the breaker opened without a panic-tier fault")
	}
}

func TestTheSameReasonOnOneGroupOrDifferentReasonsOrOldFaultsNeverTrip(t *testing.T) {
	r := newBrkRig()
	for range 5 {
		r.b.note("g1", state.ReasonSetup)
	}
	if r.b.isOpen() {
		t.Fatal("one group tripped the breaker")
	}

	r.b.note("g2", state.ReasonContract)
	r.b.note("g3", state.ReasonStateInvalid)
	if r.b.isOpen() {
		t.Fatal("three different reasons tripped the breaker")
	}

	r = newBrkRig()
	r.b.note("g1", state.ReasonSetup)
	r.b.note("g2", state.ReasonSetup)
	r.clk.Advance(breakerWindow + time.Second)
	r.b.note("g3", state.ReasonSetup)
	if r.b.isOpen() {
		t.Fatal("faults more than ten minutes apart tripped the breaker")
	}
}

func TestAPanicTierFaultFromARealInputIsWhatTheBreakerCounts(t *testing.T) {
	for _, tc := range []struct {
		in   Input
		want state.Reason
	}{
		{in(InFault, FaultData{Reason: "setup", Detail: "x"}), state.ReasonSetup},
		{in(InFault, FaultData{Reason: "contract"}), state.ReasonContract},
		{in(InFault, FaultData{Reason: "state_invalid"}), state.ReasonStateInvalid},
		{in(InGroupCrash, nil), state.ReasonCrash},
		{in(InFault, FaultData{Reason: "publish"}), ""},
	} {
		if got := Next(claimed(), tc.in, fixtureLimits()).Panic; got != tc.want {
			t.Errorf("%s: panic %q, want %q", tc.in.Kind, got, tc.want)
		}
	}
}

func TestAProbeThatEndsCleanClosesTheBreakerAndResumesTheHeldGroups(t *testing.T) {
	r := newBrkRig()
	r.trip()
	if r.b.allow("g4") || r.b.allow("g5") {
		t.Fatal("sessions started before the wait")
	}

	r.clk.Advance(breakerProbeEvery)
	if _, resumed := r.log(); !slices.Equal(resumed, []string{"g4"}) {
		t.Fatalf("the timer resumed %v, want only the oldest held group", resumed)
	}
	if !r.b.allow("g4") {
		t.Fatal("the probe was refused")
	}
	if r.b.allow("g5") || r.b.allow("g6") {
		t.Fatal("more than one session started as a probe")
	}

	r.b.ended("g4")

	ev, resumed := r.log()
	if r.b.isOpen() || !slices.Equal(ev, []string{"breaker.opened", "breaker.closed"}) {
		t.Fatalf("open %v events %v", r.b.isOpen(), ev)
	}
	if !slices.Contains(resumed, "g5") || !slices.Contains(resumed, "g6") {
		t.Fatalf("held groups resumed: %v", resumed)
	}
	if !r.b.allow("g7") {
		t.Fatal("a closed breaker held a session back")
	}
}

func TestAProbeThatHitsAPanicFaultKeepsTheBreakerOpenForAnotherWait(t *testing.T) {
	r := newBrkRig()
	r.trip()
	r.b.allow("g4")
	r.clk.Advance(breakerProbeEvery)
	if !r.b.allow("g4") {
		t.Fatal("the probe was refused")
	}

	r.b.note("g4", state.ReasonSetup)
	r.b.ended("g4")

	if !r.b.isOpen() {
		t.Fatal("a failed probe closed the breaker")
	}
	if r.b.allow("g5") {
		t.Fatal("a session started right after a failed probe")
	}

	r.clk.Advance(breakerProbeEvery)
	if !r.b.allow("g5") {
		t.Fatal("the next probe did not come after another wait")
	}
	if ev, _ := r.log(); slices.Contains(ev, "breaker.closed") {
		t.Fatalf("events %v", ev)
	}
}

func TestAHeldGroupThatStopsWorkingIsDroppedSoTheNextHeldGroupGetsTheProbe(t *testing.T) {
	r := newBrkRig()
	r.trip()
	if r.b.allow("g4") || r.b.allow("g5") {
		t.Fatal("open breaker allowed")
	}

	r.b.drop("g4") // g4 was abandoned, blocked or done while it waited
	r.clk.Advance(breakerProbeEvery)

	if _, resumed := r.log(); !slices.Equal(resumed, []string{"g5"}) {
		t.Fatalf("resumed %v, want only g5", resumed)
	}
}

func TestAProbeTurnThatStartsNoProbeHandsTheNextWaitToTheNextHeldGroup(t *testing.T) {
	r := newBrkRig()
	r.trip()
	if r.b.allow("g4") || r.b.allow("g5") {
		t.Fatal("open breaker allowed")
	}

	// The rig's resume never calls allow, as the resume of a group with nothing to start does not.
	r.clk.Advance(breakerProbeEvery)
	r.clk.Advance(breakerProbeEvery)

	if _, resumed := r.log(); !slices.Equal(resumed, []string{"g4", "g5"}) {
		t.Fatalf("resumed %v, want g4 then g5", resumed)
	}
}

func TestAResumedGroupThatAsksForTheProbeGetsIt(t *testing.T) {
	r := newBrkRig()
	r.trip()
	if r.b.allow("g4") {
		t.Fatal("open breaker allowed")
	}

	r.clk.Advance(breakerProbeEvery)
	if !r.b.allow("g4") {
		t.Fatal("the resumed group was not let through as the probe")
	}
}

func TestAnOpenBreakerHoldsBackSessionStartsAndLetsOneThroughAfterTheWait(t *testing.T) {
	x := newRig(t, [][]string{{"tg-1"}}, onlyIdle, withClock)
	for _, g := range []string{"g1", "g2", "g3"} {
		x.m.brk.note(g, state.ReasonSetup)
	}
	if !x.m.brk.isOpen() {
		t.Fatal("the breaker did not open")
	}

	if err := x.startRun("run-1", "tg-1"); err != nil {
		t.Fatal(err)
	}

	until(t, "the group to reach build", func() bool { return x.store.get("g-tg-1").Stage == StageBuild })
	time.Sleep(100 * time.Millisecond)

	// The one start so far is the group's Task Manager; the Build stage order is held.
	if n := countCmd(x.tasks.commands("g-tg-1"), task.CmdStart); n != 1 {
		t.Fatalf("starts with the breaker open: %d, want only the Task Manager's", n)
	}
	if got := x.ev.count(bus.TypeRunFailure, "breaker.opened"); got != 1 {
		t.Fatalf("breaker.opened events: %d", got)
	}

	x.clock.Advance(breakerProbeEvery)
	waitUntil(t, "the probe start", func() bool { return countCmd(x.tasks.commands("g-tg-1"), task.CmdStart) == 2 })
}
