package agent

import "testing"

// NormalizeOutput makes two runs of the same test comparable.
func TestNormalizeOutput(t *testing.T) {
	a := NormalizeOutput("--- FAIL: TestX (0.00s)\nok  \tp\t0.21s")
	b := NormalizeOutput("--- FAIL: TestX (1.25s)\nok  \tp\t(cached)")
	if a != b {
		t.Errorf("%q != %q", a, b)
	}
}
