package main

import "testing"

func TestRiskSkipsReviewOnlyForLowRiskChanges(t *testing.T) {
	diff := func(path, line string) string {
		return "diff --git a/" + path + " b/" + path + "\n--- a/" + path + "\n+++ b/" + path +
			"\n@@ -1,1 +1,2 @@\n x\n+" + line + "\n"
	}
	cases := []struct {
		name, diff string
		skip       bool
	}{
		{"docs", diff("docs/guide.md", "a new sentence"), true},
		{"code", diff("internal/calc/add.go", "func Add(a, b int) int { return a + b }"), false},
		{"workflow", diff(".github/workflows/ci.yml", "on: push"), false},
		{"empty", "", false},
	}
	for _, c := range cases {
		if got := riskSkipsReview(c.diff); got != c.skip {
			t.Errorf("%s: riskSkipsReview = %v, want %v", c.name, got, c.skip)
		}
	}
}
