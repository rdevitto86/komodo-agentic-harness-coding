package agent

// TestStatus classifies one test against the baseline.
type TestStatus string

const (
	StatusOK          TestStatus = "ok"
	StatusRegression  TestStatus = "regression"   // passed at base, fails at head: blocks
	StatusPreExisting TestStatus = "pre_existing" // fails at both: warns
	// no base result, fails at head: blocks like a regression
	StatusNew TestStatus = "new"
)

// Baseline is the test-class result at one base commit, keyed by tree hash.
type Baseline struct {
	TreeHash string
	Passed   map[string]bool // test name to whether it passed at base
}

// FlakeOutcome is the verdict on a failing test after two solo reruns.
type FlakeOutcome string

const (
	FlakeConfirmed FlakeOutcome = "confirmed" // both reruns failed
	FlakeFlaky     FlakeOutcome = "flaky"     // any rerun passed: reported, does not block
)

// Classify judges one test against the baseline; a test absent from it is new when it fails.
func (b Baseline) Classify(test string, passedAtHead bool) TestStatus {
	base, known := b.Passed[test]
	switch {
	case passedAtHead:
		return StatusOK
	case !known:
		return StatusNew
	case base:
		return StatusRegression
	default:
		return StatusPreExisting
	}
}

// ClassifyFailure judges a failing test from its two solo reruns; true means the rerun passed.
func ClassifyFailure(reruns [2]bool) FlakeOutcome {
	if !reruns[0] && !reruns[1] {
		return FlakeConfirmed
	}
	return FlakeFlaky
}
