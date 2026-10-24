package state

import "testing"

// Every reason has one stated tier; only the blocker tier is counted by a breaker.
func TestEveryReasonHasOneSeverity(t *testing.T) {
	want := map[Reason]Severity{
		ReasonNoProvider:    SeverityNone,
		ReasonPauseTimeout:  SeverityNone,
		ReasonPaidRefused:   SeverityNone,
		ReasonFlap:          SeverityNone,
		ReasonRetries:       SeverityNone,
		ReasonNeedsScope:    SeverityNone,
		ReasonRiskyChange:   SeverityNone,
		ReasonContext:       SeverityNone,
		ReasonSetup:         SeverityBlocker,
		ReasonPublish:       SeverityFault,
		ReasonMergeConflict: SeverityNone,
		ReasonCleanup:       SeverityFault,
		ReasonWall:          SeverityNone,
		ReasonDependency:    SeverityNone,
		ReasonCrash:         SeverityBlocker,
		ReasonContract:      SeverityBlocker,
		ReasonStateInvalid:  SeverityBlocker,
		ReasonDone:          SeverityNone,
		ReasonCheckFail:     SeverityWork,
		ReasonNoResult:      SeverityWork,
		ReasonSchemaFail:    SeverityWork,
		ReasonTimeout:       SeverityWork,
		ReasonIdle:          SeverityWork,
		ReasonRefusals:      SeverityWork,
		ReasonProvider:      SeverityMinor,
		ReasonBilling:       SeverityMinor,
		ReasonPaused:        SeverityMinor,
		ReasonCancelled:     SeverityMinor,
	}

	if len(want) != len(reasonValues) {
		t.Fatalf("the table lists %d reasons, the closed list has %d", len(want), len(reasonValues))
	}

	for _, r := range reasonValues {
		got, ok := want[r]
		if !ok {
			t.Errorf("%s has no stated tier", r)
		}
		if r.Severity() != got {
			t.Errorf("%s: severity %q, want %q", r, r.Severity(), got)
		}
	}
}

// Work outcomes, rate limits and restarts never reach the blocker tier, so no breaker counts them.
func TestWorkOutcomesAndRateLimitsAreNeverBlockers(t *testing.T) {
	for _, r := range []Reason{
		ReasonTimeout, ReasonIdle, ReasonCheckFail, ReasonNoResult, ReasonSchemaFail, ReasonRefusals,
		ReasonProvider, ReasonPaused, ReasonCancelled, ReasonBilling, ReasonFlap, ReasonRetries,
	} {
		if Classify(r) == SeverityBlocker {
			t.Errorf("%s is a blocker", r)
		}
	}
}
