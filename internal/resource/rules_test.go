package resource

import (
	"reflect"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// base is a fixed morning in the machine's local time zone.
var base = time.Date(2026, 10, 14, 10, 0, 0, 0, time.Local)

// defaults are the shipped thresholds with no ceilings and no daily amount.
var defaults = Thresholds{QuotaPct: 80, USDPerRun: 5, GroupTokens: 2_000_000}

// ev builds a resource event for a run and group.
func ev(sub bus.Subtype, run, group string, data bus.Payload) bus.Event {
	return bus.Event{Type: bus.TypeResource, Subtype: sub, Run: bus.ID(run), Group: bus.ID(group), Data: data}
}

// head builds a headroom event whose window is pct percent used.
func head(link, run, group string, pct int, reset time.Time) bus.Event {
	return ev(subtypeHeadroom, run, group, &Headroom{Link: link, Window: "five_hour", Limit: 100, Remaining: int64(100 - pct), ResetAt: reset})
}

// paid builds a usage event that spent usd on link.
func paid(link, run, group string, usd float64) bus.Event {
	return ev(subtypeUsage, run, group, &Usage{Link: link, USD: usd})
}

// toks builds a usage event of n input tokens on link.
func toks(link, run, group string, n int64) bus.Event {
	return ev(subtypeUsage, run, group, &Usage{Link: link, TokensIn: n})
}

// drive applies e, evaluates the rules and marks every order delivered, as a run with no failing calls would.
func drive(t *testing.T, s *State, th Thresholds, e bus.Event, now time.Time) Result {
	t.Helper()
	if err := check(e); err != nil {
		t.Fatal(err)
	}
	o, _, ok := s.apply(e, now)
	if !ok {
		return Result{}
	}
	res := Evaluate(s, th, o)
	for _, er := range res.Errors {
		s.markRaised(o, er)
	}
	for _, p := range res.Pauses {
		if s.errorRaised(o, p) {
			s.markPaused(o, p)
		}
	}
	for _, n := range res.Nudges {
		s.markNudged(n)
	}
	return res
}

// kinds lists the kinds of the errors in r.
func kinds(r Result) []string {
	var out []string
	for _, e := range r.Errors {
		out = append(out, e.Kind)
	}
	return out
}

// wantNone fails unless r holds nothing.
func wantNone(t *testing.T, r Result) {
	t.Helper()
	if len(r.Errors) != 0 || len(r.Pauses) != 0 || len(r.Nudges) != 0 {
		t.Fatalf("want nothing, got %+v", r)
	}
}

// wantKinds fails unless r holds exactly the given error kinds, in order.
func wantKinds(t *testing.T, r Result, want ...string) {
	t.Helper()
	if got := kinds(r); !reflect.DeepEqual(got, want) && (len(got) != 0 || len(want) != 0) {
		t.Fatalf("error kinds %v, want %v", got, want)
	}
}

// TestWindowWarnOnce: 79 percent is quiet, 80 warns once, 90 does not warn again.
func TestWindowWarnOnce(t *testing.T) {
	s, reset := NewState(), base.Add(2*time.Hour)
	wantNone(t, drive(t, s, defaults, head("claude-cli", "r1", "g1", 79, reset), base))
	r := drive(t, s, defaults, head("claude-cli", "r1", "g1", 80, reset), base)
	wantKinds(t, r, "window")
	e := r.Errors[0]
	if e.Severity != "warn" || e.Value != 80 || e.Threshold != 80 || e.Link != "claude-cli" || e.Run != "r1" || !e.ResetAt.Equal(reset) {
		t.Fatalf("warning %+v", e)
	}
	if len(r.Pauses) != 0 {
		t.Fatalf("a warning at 80 percent paused: %+v", r.Pauses)
	}
	wantNone(t, drive(t, s, defaults, head("claude-cli", "r1", "g1", 90, reset), base))
}

// TestWindowFullPausesOnce: 100 percent raises window_full and one pause order for the link's groups, once.
func TestWindowFullPausesOnce(t *testing.T) {
	s, reset := NewState(), base.Add(3*time.Hour)
	drive(t, s, defaults, head("claude-cli", "r1", "g2", 10, reset), base)
	drive(t, s, defaults, head("claude-cli", "r1", "g1", 20, reset), base)
	drive(t, s, defaults, head("anthropic-api", "r1", "g3", 20, reset), base)
	r := drive(t, s, defaults, head("claude-cli", "r1", "g1", 100, reset), base)
	wantKinds(t, r, "window_full")
	if r.Errors[0].Severity != "warn" || r.Errors[0].Threshold != 100 {
		t.Fatalf("window_full %+v", r.Errors[0])
	}
	want := PauseOrder{Reason: "window_full", Link: "claude-cli", Groups: []string{"g1", "g2"}, Until: reset}
	if len(r.Pauses) != 1 || !reflect.DeepEqual(r.Pauses[0], want) {
		t.Fatalf("pauses %+v, want %+v", r.Pauses, want)
	}
	wantNone(t, drive(t, s, defaults, head("claude-cli", "r1", "g1", 100, reset), base))
}

// TestWindowJumpToFull: a first reading at 100 raises window_full and not the 80 percent warning.
func TestWindowJumpToFull(t *testing.T) {
	s := NewState()
	r := drive(t, s, defaults, head("claude-cli", "r1", "g1", 100, base.Add(time.Hour)), base)
	wantKinds(t, r, "window_full")
}

// TestWindowRearmsAfterReset: once the reset time passes the next reading at 81 warns again.
func TestWindowRearmsAfterReset(t *testing.T) {
	s, reset := NewState(), base.Add(time.Hour)
	wantKinds(t, drive(t, s, defaults, head("claude-cli", "r1", "g1", 85, reset), base), "window")
	later := base.Add(2 * time.Hour)
	wantKinds(t, drive(t, s, defaults, head("claude-cli", "r1", "g1", 81, later.Add(5*time.Hour)), later), "window")
}

// TestResetClearsLinkGroups: a window reset empties the groups seen on the link.
func TestResetClearsLinkGroups(t *testing.T) {
	s, reset := NewState(), base.Add(time.Hour)
	drive(t, s, defaults, head("claude-cli", "r1", "gold", 10, reset), base)
	later := base.Add(2 * time.Hour)
	drive(t, s, defaults, head("claude-cli", "r1", "gnew", 100, later.Add(time.Hour)), later)
	if got := names(s.LinkGroups["claude-cli"]); !reflect.DeepEqual(got, []string{"gnew"}) {
		t.Fatalf("link groups %v, want [gnew]", got)
	}
}

// TestHeadroomLimitZero: a reading with no window total is ignored and raises nothing.
func TestHeadroomLimitZero(t *testing.T) {
	s := NewState()
	e := ev(subtypeHeadroom, "r1", "g1", &Headroom{Link: "claude-cli", Window: "five_hour", Limit: 0, Remaining: 0, ResetAt: base.Add(time.Hour)})
	wantNone(t, drive(t, s, defaults, e, base))
	if len(s.Windows) != 0 || len(s.LinkGroups) != 0 {
		t.Fatalf("an ignored reading changed the state: %+v", s)
	}
}

// TestSpendRunWarnsOncePerRun: 3.99 is quiet, 4.01 warns, and the same dollars across two runs warn neither.
func TestSpendRunWarnsOncePerRun(t *testing.T) {
	s := NewState()
	wantNone(t, drive(t, s, defaults, paid("anthropic-api", "r1", "g1", 3.99), base))
	r := drive(t, s, defaults, paid("anthropic-api", "r1", "g1", 0.02), base)
	wantKinds(t, r, "spend_run")
	if e := r.Errors[0]; e.Run != "r1" || e.Threshold != 4 || e.Value < 4.00 || e.Value > 4.02 {
		t.Fatalf("warning %+v", e)
	}
	wantNone(t, drive(t, s, defaults, paid("anthropic-api", "r1", "g1", 0.5), base))

	s = NewState()
	wantNone(t, drive(t, s, defaults, paid("anthropic-api", "r1", "g1", 3.99), base))
	wantNone(t, drive(t, s, defaults, paid("anthropic-api", "r2", "g2", 3.99), base))
}

// TestNoCeilingNoPause: 50 dollars in one run warns and orders no pause.
func TestNoCeilingNoPause(t *testing.T) {
	s := NewState()
	r := drive(t, s, defaults, paid("anthropic-api", "r1", "g1", 50), base)
	wantKinds(t, r, "spend_run")
	if len(r.Pauses) != 0 {
		t.Fatalf("paid spend without a ceiling paused: %+v", r.Pauses)
	}
}

// TestRunCeiling: reaching the per-run ceiling raises spend_ceiling and one pause order naming the run's groups.
func TestRunCeiling(t *testing.T) {
	th := defaults
	th.USDPerRun, th.USDCeilingPerRun = 0, 10
	s := NewState()
	drive(t, s, th, paid("anthropic-api", "r1", "g2", 4), base)
	drive(t, s, th, paid("anthropic-api", "r1", "g1", 5.5), base)
	r := drive(t, s, th, paid("anthropic-api", "r1", "g1", 0.5), base)
	wantKinds(t, r, "spend_ceiling")
	want := PauseOrder{Reason: "spend_ceiling", Run: "r1", Groups: []string{"g1", "g2"}}
	if len(r.Pauses) != 1 || !reflect.DeepEqual(r.Pauses[0], want) {
		t.Fatalf("pauses %+v, want %+v", r.Pauses, want)
	}
	wantNone(t, drive(t, s, th, paid("anthropic-api", "r1", "g1", 1), base))
	wantNone(t, drive(t, s, th, paid("anthropic-api", "r3", "g9", 9), base))
}

// TestSubscriptionEquivalentIsNotSpend: a usage event with usd 0 and usd_equiv 12 changes no spend total.
func TestSubscriptionEquivalentIsNotSpend(t *testing.T) {
	s := NewState()
	e := ev(subtypeUsage, "r1", "g1", &Usage{Link: "claude-cli", USD: 0, USDEquiv: 12})
	wantNone(t, drive(t, s, defaults, e, base))
	if s.Spend.RunUSD["r1"] != 0 || s.Spend.DayUSD != 0 {
		t.Fatalf("spend %+v", s.Spend)
	}
}

// TestDayChange: a new local day resets the day's total and flag so the warning can fire again.
func TestDayChange(t *testing.T) {
	th := defaults
	th.USDPerRun, th.USDPerDay = 0, 10
	s := NewState()
	wantKinds(t, drive(t, s, th, paid("anthropic-api", "r1", "g1", 8), base), "spend_day")
	wantNone(t, drive(t, s, th, paid("anthropic-api", "r1", "g1", 1), base))
	next := base.Add(24 * time.Hour)
	wantNone(t, drive(t, s, th, paid("anthropic-api", "r1", "g1", 1), next))
	if s.Spend.DayUSD != 1 || s.Spend.DayWarned || s.Spend.Day != dayOf(next) {
		t.Fatalf("spend after the day change %+v", s.Spend)
	}
	wantKinds(t, drive(t, s, th, paid("anthropic-api", "r1", "g1", 7), next), "spend_day")
}

// TestGroupTokenLadder: 1,799,999 is quiet, 1,800,000 nudges once, 2,000,000 warns and pauses once.
func TestGroupTokenLadder(t *testing.T) {
	s := NewState()
	wantNone(t, drive(t, s, defaults, toks("claude-cli", "r1", "g1", 1_799_999), base))
	r := drive(t, s, defaults, toks("claude-cli", "r1", "g1", 1), base)
	wantKinds(t, r)
	if len(r.Nudges) != 1 || !reflect.DeepEqual(r.Nudges[0], NudgeOrder{Groups: []string{"g1"}}) || len(r.Pauses) != 0 {
		t.Fatalf("at 90 percent: %+v", r)
	}
	wantNone(t, drive(t, s, defaults, toks("claude-cli", "r1", "g1", 1000), base))
	r = drive(t, s, defaults, toks("claude-cli", "r1", "g1", 199_000), base)
	wantKinds(t, r, "group_tokens")
	e := r.Errors[0]
	if e.Group != "g1" || e.Value != 2_000_000 || e.Threshold != 2_000_000 || e.Severity != "warn" {
		t.Fatalf("group_tokens %+v", e)
	}
	if len(r.Pauses) != 1 || r.Pauses[0].Reason != "group_tokens" || !reflect.DeepEqual(r.Pauses[0].Groups, []string{"g1"}) || len(r.Nudges) != 0 {
		t.Fatalf("at the cap: %+v", r)
	}
	wantNone(t, drive(t, s, defaults, toks("claude-cli", "r1", "g1", 5), base))
}

// TestGroupTokensOffAtZero: with no cap there is no nudge, warning or pause.
func TestGroupTokensOffAtZero(t *testing.T) {
	th := defaults
	th.GroupTokens = 0
	s := NewState()
	for _, n := range []int64{1_800_000, 200_000, 9_000_000} {
		wantNone(t, drive(t, s, th, toks("claude-cli", "r1", "g1", n), base))
	}
}

// TestCacheReadNotCounted: cache reads do not count toward a group's tokens.
func TestCacheReadNotCounted(t *testing.T) {
	s := NewState()
	e := ev(subtypeUsage, "r1", "g1", &Usage{Link: "claude-cli", TokensIn: 10, TokensOut: 20, CacheWrite: 30, CacheRead: 5_000_000})
	wantNone(t, drive(t, s, defaults, e, base))
	if s.Spend.GroupTokens["g1"] != 60 {
		t.Fatalf("group tokens %d, want 60", s.Spend.GroupTokens["g1"])
	}
}

// TestGrantMovesTheLadder: after a grant the next nudge is at 90 percent of twice the cap.
func TestGrantMovesTheLadder(t *testing.T) {
	m := New(defaults, Deps{})
	s := m.st
	wantKinds(t, drive(t, s, defaults, toks("claude-cli", "r1", "g1", 2_000_000), base), "group_tokens")
	m.GrantGroupTokens("g1")
	wantNone(t, drive(t, s, defaults, toks("claude-cli", "r1", "g1", 1), base))
	wantNone(t, drive(t, s, defaults, toks("claude-cli", "r1", "g1", 1_599_998), base))
	r := drive(t, s, defaults, toks("claude-cli", "r1", "g1", 1), base)
	if len(r.Nudges) != 1 || len(r.Errors) != 0 {
		t.Fatalf("at 90%% of twice the cap: %+v", r)
	}
	r = drive(t, s, defaults, toks("claude-cli", "r1", "g1", 2_000_000), base)
	wantKinds(t, r, "group_tokens")
	if r.Errors[0].Threshold != 4_000_000 {
		t.Fatalf("threshold %v, want 4000000", r.Errors[0].Threshold)
	}
}

// TestDayCeiling: the daily ceiling pauses only groups on a paid link until the next local midnight, once per day.
func TestDayCeiling(t *testing.T) {
	th := defaults
	th.USDPerRun, th.USDCeilingPerDay = 0, 20
	s := NewState()
	drive(t, s, th, ev(subtypeUsage, "r1", "gsub", &Usage{Link: "claude-cli", USDEquiv: 3}), base)
	drive(t, s, th, paid("anthropic-api", "r1", "gpaid2", 5), base)
	r := drive(t, s, th, paid("anthropic-api", "r2", "gpaid1", 15), base)
	wantKinds(t, r, "spend_ceiling_day")
	want := PauseOrder{Reason: "spend_ceiling_day", Groups: []string{"gpaid1", "gpaid2"}, Until: nextMidnight(base)}
	if len(r.Pauses) != 1 || !reflect.DeepEqual(r.Pauses[0], want) {
		t.Fatalf("pauses %+v, want %+v", r.Pauses, want)
	}
	wantNone(t, drive(t, s, th, paid("anthropic-api", "r2", "gpaid1", 1), base))
	next := base.Add(24 * time.Hour)
	wantNone(t, drive(t, s, th, paid("anthropic-api", "r2", "gpaid1", 19), next))
	wantKinds(t, drive(t, s, th, paid("anthropic-api", "r2", "gpaid1", 1), next), "spend_ceiling_day")
}

// TestOverageAndLimitOnce: overage and limit each raise once until the reset time moves or passes.
func TestOverageAndLimitOnce(t *testing.T) {
	s, reset := NewState(), base.Add(time.Hour)
	over := ev(subtypeOverage, "r1", "g1", &Overage{Link: "claude-cli", ResetAt: reset})
	r := drive(t, s, defaults, over, base)
	wantKinds(t, r, "overage")
	if r.Errors[0].Severity != "warn" || len(r.Pauses) != 0 {
		t.Fatalf("overage %+v", r)
	}
	wantNone(t, drive(t, s, defaults, over, base))
	later := base.Add(2 * time.Hour)
	wantKinds(t, drive(t, s, defaults, ev(subtypeOverage, "r1", "g1", &Overage{Link: "claude-cli", ResetAt: later.Add(time.Hour)}), later), "overage")

	lim := ev(subtypeLimit, "r1", "g1", &Limit{Link: "claude-cli", Kind: "window", ResetAt: reset})
	r = drive(t, s, defaults, lim, base)
	wantKinds(t, r, "limit")
	if r.Errors[0].Severity != "error" || !r.Errors[0].ResetAt.Equal(reset) || len(r.Pauses) != 0 {
		t.Fatalf("limit %+v", r)
	}
	wantNone(t, drive(t, s, defaults, lim, base))
	other := ev(subtypeLimit, "r1", "g1", &Limit{Link: "claude-cli", Kind: "weekly", ResetAt: reset})
	wantKinds(t, drive(t, s, defaults, other, base), "limit")
	moved := ev(subtypeLimit, "r1", "g1", &Limit{Link: "claude-cli", Kind: "window", ResetAt: reset.Add(time.Hour)})
	wantKinds(t, drive(t, s, defaults, moved, base), "limit")
}

// TestLinksDoNotShareFlags: a warning on one link leaves the other link able to warn.
func TestLinksDoNotShareFlags(t *testing.T) {
	s, reset := NewState(), base.Add(time.Hour)
	wantKinds(t, drive(t, s, defaults, head("claude-cli", "r1", "g1", 85, reset), base), "window")
	wantKinds(t, drive(t, s, defaults, head("anthropic-api", "r1", "g2", 85, reset), base), "window")
}

// TestEvaluateIsPure: the same state and observation give the same result and leave the state able to raise it again.
func TestEvaluateIsPure(t *testing.T) {
	s := NewState()
	e := head("claude-cli", "r1", "g1", 100, base.Add(time.Hour))
	o, _, _ := s.apply(e, base)
	a, b := Evaluate(s, defaults, o), Evaluate(s, defaults, o)
	if len(a.Errors) != 1 || len(a.Pauses) != 1 || !reflect.DeepEqual(a, b) {
		t.Fatalf("first %+v second %+v", a, b)
	}
}
