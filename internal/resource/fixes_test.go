package resource

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// TestLateGroupPausedPastRunCeiling: a group that first appears after the run ceiling was reached gets its own pause order.
func TestLateGroupPausedPastRunCeiling(t *testing.T) {
	th := defaults
	th.USDPerRun, th.USDCeilingPerRun = 0, 10
	s := NewState()
	r := drive(t, s, th, paid("anthropic-api", "r1", "ga", 10), base)
	if len(r.Pauses) != 1 || !reflect.DeepEqual(r.Pauses[0].Groups, []string{"ga"}) {
		t.Fatalf("first order %+v", r.Pauses)
	}
	r = drive(t, s, th, paid("anthropic-api", "r1", "gb", 6), base)
	if len(r.Errors) != 0 || len(r.Pauses) != 1 || !reflect.DeepEqual(r.Pauses[0].Groups, []string{"gb"}) {
		t.Fatalf("late group: %+v", r)
	}
	wantNone(t, drive(t, s, th, paid("anthropic-api", "r1", "gb", 1), base))
}

// TestLateGroupPausedPastFullWindowAndDayCeiling: a group that joins a full window or a day over its ceiling is paused
// alone.
func TestLateGroupPausedPastFullWindowAndDayCeiling(t *testing.T) {
	th := defaults
	th.USDPerRun, th.USDCeilingPerDay = 0, 20
	s, reset := NewState(), base.Add(time.Hour)
	drive(t, s, th, head("claude-cli", "r1", "ga", 100, reset), base)
	r := drive(t, s, th, head("claude-cli", "r1", "gb", 100, reset), base)
	if len(r.Pauses) != 1 || !reflect.DeepEqual(r.Pauses[0].Groups, []string{"gb"}) {
		t.Fatalf("late group on a full window: %+v", r)
	}
	drive(t, s, th, paid("anthropic-api", "r1", "gc", 20), base)
	r = drive(t, s, th, paid("anthropic-api", "r1", "gd", 1), base)
	if len(r.Pauses) != 1 || !reflect.DeepEqual(r.Pauses[0].Groups, []string{"gd"}) {
		t.Fatalf("late group past the day ceiling: %+v", r)
	}
}

// TestSiblingResetKeepsGroups: groups seen before one window of a link resets are still named when another window of it
// fills.
func TestSiblingResetKeepsGroups(t *testing.T) {
	s := NewState()
	drive(t, s, defaults, head("claude-cli", "r1", "g1", 10, base.Add(time.Hour)), base)
	week := ev(subtypeHeadroom, "r1", "g2", &Headroom{
		Link:      "claude-cli",
		Window:    "weekly",
		Limit:     100,
		Remaining: 90,
		ResetAt:   base.Add(10 * time.Hour),
	})
	drive(t, s, defaults, week, base)
	later := base.Add(2 * time.Hour)
	drive(t, s, defaults, head("claude-cli", "r1", "g3", 10, later.Add(5*time.Hour)), later)
	week = ev(subtypeHeadroom, "r1", "g3", &Headroom{
		Link:      "claude-cli",
		Window:    "weekly",
		Limit:     100,
		Remaining: 0,
		ResetAt:   base.Add(10 * time.Hour),
	})
	r := drive(t, s, defaults, week, later)
	if len(r.Pauses) != 1 || !reflect.DeepEqual(r.Pauses[0].Groups, []string{"g1", "g2", "g3"}) {
		t.Fatalf("pause %+v, want g1 g2 g3", r.Pauses)
	}
}

// TestBadHeadroomIsMalformed: a negative total, a negative remainder or a remainder above the total is refused and changes
// nothing.
func TestBadHeadroomIsMalformed(t *testing.T) {
	r := newRig(defaults)
	for _, h := range []Headroom{
		{Link: "claude-cli", Window: "w", Limit: -5, Remaining: 1},
		{Link: "claude-cli", Window: "w", Limit: 100, Remaining: -1},
		{Link: "claude-cli", Window: "w", Limit: 100, Remaining: 101},
	} {
		h.ResetAt = base.Add(time.Hour)
		if err := r.observe(ev(subtypeHeadroom, "r1", "g1", &h)); !errors.Is(err, errMalformed) {
			t.Fatalf("%+v: err %v, want errMalformed", h, err)
		}
	}
	if len(r.m.st.Windows) != 0 || len(r.th.reports) != 3 {
		t.Fatalf("windows %v reports %d", r.m.st.Windows, len(r.th.reports))
	}
}

// TestFullWindowPauseRetriedByUsage: a pause that failed, or had no group to name, is sent by the next event that names a
// group on the link.
func TestFullWindowPauseRetriedByUsage(t *testing.T) {
	r := newRig(defaults)
	r.rt.failPause = 1
	reset := base.Add(time.Hour)
	_ = r.observe(head("claude-cli", "r1", "g1", 100, reset))
	if err := r.observe(ev(subtypeUsage, "r1", "g1", &Usage{Link: "claude-cli", TokensIn: 1})); err != nil {
		t.Fatal(err)
	}
	if want := []string{"throw:window_full", "pause:window_full", "pause:window_full"}; !reflect.DeepEqual(r.c.all(), want) {
		t.Fatalf("calls %v, want %v", r.c.all(), want)
	}

	r = newRig(defaults)
	if err := r.observe(head("claude-cli", "r1", "", 100, reset)); err != nil {
		t.Fatal(err)
	}
	if err := r.observe(ev(subtypeUsage, "r1", "g1", &Usage{Link: "claude-cli", TokensIn: 1})); err != nil {
		t.Fatal(err)
	}
	if want := []string{"throw:window_full", "pause:window_full"}; !reflect.DeepEqual(r.c.all(), want) {
		t.Fatalf("calls %v, want %v", r.c.all(), want)
	}
}

// TestGrantWaitsForObserve: a grant made while an event is being delivered waits for it, so it cannot be overwritten.
func TestGrantWaitsForObserve(t *testing.T) {
	r := newRig(defaults)
	release := make(chan struct{})
	r.rt.block = release
	observed := make(chan struct{})
	go func() {
		_ = r.observe(toks("claude-cli", "r1", "g1", 2_000_000))
		close(observed)
	}()
	for len(r.c.all()) < 2 {
		time.Sleep(time.Millisecond)
	}
	granted := make(chan struct{})
	go func() { r.m.GrantGroupTokens("g1"); close(granted) }()
	select {
	case <-granted:
		t.Fatal("the grant ran while the pause was being delivered")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-observed
	<-granted
	if r.m.st.Spend.GroupCapped["g1"] || r.m.st.Spend.CapGrants["g1"] != 1 {
		t.Fatalf("spend after the grant %+v", r.m.st.Spend)
	}
}

// TestZeroResetRaisesEachTime: an overage or limit with no reset time sets no lasting flag, so it raises every time.
func TestZeroResetRaisesEachTime(t *testing.T) {
	r := newRig(defaults)
	for range 2 {
		_ = r.observe(ev(subtypeLimit, "r1", "g1", &Limit{Link: "claude-cli", Kind: "spend"}))
		_ = r.observe(ev(subtypeOverage, "r1", "g1", &Overage{Link: "claude-cli"}))
	}
	if want := []string{
		"throw:limit",
		"throw:overage",
		"throw:limit",
		"throw:overage",
	}; !reflect.DeepEqual(r.c.all(), want) {
		t.Fatalf("calls %v, want %v", r.c.all(), want)
	}
}

// TestDeliveryFailuresReported: a failed throw, pause or nudge reaches the Error Handler as an ordinary error.
func TestDeliveryFailuresReported(t *testing.T) {
	r := newRig(defaults)
	r.th.failN, r.rt.failPause, r.rt.failNudge = 1, 1, 1
	_ = r.observe(head("claude-cli", "r1", "g1", 100, base.Add(time.Hour)))
	_ = r.observe(head("claude-cli", "r1", "g1", 100, base.Add(time.Hour)))
	_ = r.observe(toks("claude-cli", "r1", "g1", 1_800_000))
	if len(r.th.reports) != 3 {
		t.Fatalf("reports %v, want 3", r.th.reports)
	}
}

// TestEndRunAndEndGroupPrune: a finished run or group leaves no totals, flags or group-list entries behind.
func TestEndRunAndEndGroupPrune(t *testing.T) {
	th := defaults
	th.USDCeilingPerRun = 4
	r := newRig(th)
	_ = r.observe(head("claude-cli", "r1", "g1", 10, base.Add(time.Hour)))
	_ = r.observe(ev(subtypeUsage, "r1", "g1", &Usage{Link: "anthropic-api", USD: 5, TokensIn: 2_000_000}))
	r.m.GrantGroupTokens("g1")
	r.m.EndGroup("g1")
	s := &r.m.st.Spend
	if len(s.GroupTokens)+len(s.GroupNudged)+len(s.GroupCapped)+len(s.GroupCapRaised)+len(s.CapGrants) != 0 {
		t.Fatalf("group maps %+v", s)
	}
	if len(r.m.st.RunGroups["r1"]) != 0 || len(r.m.st.LinkGroups["claude-cli"]) != 0 {
		t.Fatalf("group lists %v %v", r.m.st.RunGroups, r.m.st.LinkGroups)
	}
	r.m.EndRun("r1")
	if len(s.RunUSD)+len(s.RunWarned)+len(s.RunCeiling)+len(s.RunCeilingRaised) != 0 || len(r.m.st.RunGroups) != 0 {
		t.Fatalf("run maps %+v %v", s, r.m.st.RunGroups)
	}
}

// TestUsageWithoutRunIsMalformed: spend belongs to a run, so a usage event with no run is refused and sums into nothing.
func TestUsageWithoutRunIsMalformed(t *testing.T) {
	r := newRig(defaults)
	if err := r.observe(paid("anthropic-api", "", "g1", 3)); !errors.Is(err, errMalformed) {
		t.Fatalf("err %v, want errMalformed", err)
	}
	if len(r.m.st.Spend.RunUSD) != 0 {
		t.Fatalf("run spend %v", r.m.st.Spend.RunUSD)
	}
}

// TestProviderWindowNamesDoNotCollide: a provider window may carry any name, including the names the link-level notices once
// used.
func TestProviderWindowNamesDoNotCollide(t *testing.T) {
	r := newRig(defaults)
	reset := base.Add(time.Hour)
	for _, name := range []string{"overage", "limit:window"} {
		h := ev(subtypeHeadroom, "r1", "g1", &Headroom{
			Link:      "claude-cli",
			Window:    name,
			Limit:     100,
			Remaining: 50,
			ResetAt:   reset,
		})
		if err := r.observe(h); err != nil {
			t.Fatal(err)
		}
	}
	_ = r.observe(ev(subtypeOverage, "r1", "g1", &Overage{Link: "claude-cli", ResetAt: reset.Add(time.Hour)}))
	_ = r.observe(ev(subtypeLimit, "r1", "g1", &Limit{Link: "claude-cli", Kind: "window", ResetAt: reset.Add(time.Hour)}))
	for _, name := range []string{"overage", "limit:window"} {
		if w := r.m.st.Windows[WindowKey{Link: "claude-cli", Window: name}]; w == nil || !w.ResetAt.Equal(reset) {
			t.Fatalf("window %q was overwritten: %+v", name, w)
		}
	}
}
