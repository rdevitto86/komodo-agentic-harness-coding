package state

import "testing"

// Every reason has one stated tier, and the panic, fault and minor lists are exactly the ones the design names.
func TestEveryReasonHasOneSeverity(t *testing.T) {
	want := map[Reason]Severity{
		ReasonCrash:        SeverityPanic,
		ReasonStateInvalid: SeverityPanic,
		ReasonContract:     SeverityPanic,
		ReasonSetup:        SeverityPanic,
		ReasonPublish:      SeverityFault,
		ReasonCleanup:      SeverityFault,
		ReasonCancelled:    SeverityMinor,
		ReasonProvider:     SeverityMinor,
		ReasonPaused:       SeverityMinor,
		ReasonBilling:      SeverityMinor,
		ReasonTimeout:      SeverityWork,
		ReasonIdle:         SeverityWork,
		ReasonCheckFail:    SeverityWork,
		ReasonNoResult:     SeverityWork,
		ReasonSchemaFail:   SeverityWork,
		ReasonRefusals:     SeverityWork,
	}

	for _, r := range reasonValues {
		if got := r.Severity(); got != want[r] {
			t.Errorf("%s: severity %q, want %q", r, got, want[r])
		}
	}
}

// The block-only reasons about the work are in no tier, and a lost session process is a fault, not a panic.
func TestWorkBlocksAreNoFaultAndASessionCrashIsAFault(t *testing.T) {
	for _, r := range []Reason{
		ReasonFlap, ReasonNeedsScope, ReasonContext, ReasonMergeConflict, ReasonDependency, ReasonTester,
		ReasonFlaky, ReasonRetries, ReasonNoProvider, ReasonPauseTimeout, ReasonPaidRefused,
	} {
		if got := r.Severity(); got != SeverityNone {
			t.Errorf("%s: severity %q, want none", r, got)
		}
	}

	if got := Classify(ReasonCrash, true); got != SeverityFault {
		t.Errorf("session crash: %q", got)
	}
}
