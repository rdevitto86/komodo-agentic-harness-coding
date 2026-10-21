package task

import "testing"

// Three failures that differ only in their timings are the same error and end the session.
func TestSameGoTestFailureWithDifferentTimingsCountsAsTheSameError(t *testing.T) {
	var c checkCap

	outs := []string{
		"--- FAIL: TestX (0.00s)\nFAIL\tp\t0.21s",
		"--- FAIL: TestX (0.02s)\nFAIL\tp\t0.30s",
		"--- FAIL: TestX (0.01s)\nFAIL\tp\t(cached)",
	}

	var v capVerdict
	for _, o := range outs {
		v = c.record("test", 1, o)
	}

	if v.end == "" {
		t.Fatal("three failures with different timings did not end the session")
	}
}
