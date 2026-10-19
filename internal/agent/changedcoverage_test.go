package agent

import (
	"context"
	"slices"
	"testing"
)

// addedDiff adds lines 2 to 6 of a/a.go.
const addedDiff = "diff --git a/a/a.go b/a/a.go\n--- a/a/a.go\n+++ b/a/a.go\n@@ -1,2 +1,7 @@\n package a\n" +
	"+func f(x int) int {\n+\tif x == 0 {\n+\t\treturn -1\n+\t}\n+\treturn x\n+}\n x\n"

// goProfile covers lines 2, 3, 6 and 7 of a/a.go and leaves line 4 uncovered.
const goProfile = "mode: set\nm/a/a.go:2.20,3.12 2 1\nm/a/a.go:3.12,5.3 1 0\nm/a/a.go:6.2,7.2 1 1\n" +
	"m/b/b.go:1.1,9.2 3 0\n"

func TestParseLineCoverageGo(t *testing.T) {
	lc, ok := ParseLineCoverage("go", "ok\ntotal:\t(statements)\t50.0%\n"+goProfile)
	if !ok {
		t.Fatal("no line data from a Go profile")
	}
	if !lc["m/a/a.go"][2] || !lc["m/a/a.go"][6] {
		t.Errorf("covered lines missing: %v", lc["m/a/a.go"])
	}
	if cov, exec := lc["m/a/a.go"][4]; cov || !exec {
		t.Errorf("line 4 is executable and uncovered: %v %v", cov, exec)
	}
	if _, exec := lc["m/a/a.go"][1]; exec {
		t.Error("line 1 has no block")
	}
	if _, ok := ParseLineCoverage("go", "ok\ntotal:\t(statements)\t50.0%\n"); ok {
		t.Error("line data from output with no profile")
	}
}

func TestChangedCoverageListsUncoveredAddedLines(t *testing.T) {
	lc, _ := ParseLineCoverage("go", goProfile)
	got := ChangedCoverage(addedDiff, lc)
	if !slices.Equal(got.Uncovered, []string{"a/a.go:4-5"}) || got.Executable != 6 || got.Covered != 4 {
		t.Errorf("changed coverage = %+v", got)
	}
	if got := ChangedCoverage("", lc); got.Executable != 0 || len(got.Uncovered) != 0 {
		t.Errorf("empty diff = %+v", got)
	}
}

func TestParseLineCoveragePython(t *testing.T) {
	out := "...\n{\"files\": {\"pkg/a.py\": {\"executed_lines\": [1, 2], \"missing_lines\": [3, 4]}}, " +
		"\"totals\": {\"percent_covered\": 50.0}}\n"
	lc, ok := ParseLineCoverage("python", out)
	if !ok || !lc["pkg/a.py"][2] || lc["pkg/a.py"][3] {
		t.Fatalf("python lines: %v %v", lc, ok)
	}
	diff := "+++ b/pkg/a.py\n@@ -0,0 +1,4 @@\n+a\n+b\n+c\n+d\n"
	if got := ChangedCoverage(diff, lc); !slices.Equal(got.Uncovered, []string{"pkg/a.py:3-4"}) || got.Covered != 2 {
		t.Errorf("python changed coverage = %+v", got)
	}
}

func TestParseLineCoverageJaCoCo(t *testing.T) {
	out := "<report><package name=\"com/x\"><sourcefile name=\"Foo.java\"><line nr=\"3\" mi=\"0\" ci=\"2\" mb=\"0\" cb=\"0\"/>" +
		"<line nr=\"4\" mi=\"2\" ci=\"0\" mb=\"0\" cb=\"0\"/><counter type=\"LINE\" missed=\"1\" covered=\"1\"/></sourcefile>" +
		"</package></report>"
	lc, ok := ParseLineCoverage("java", out)
	if !ok || !lc["com/x/Foo.java"][3] || lc["com/x/Foo.java"][4] {
		t.Fatalf("jacoco lines: %v %v", lc, ok)
	}
	diff := "+++ b/src/main/java/com/x/Foo.java\n@@ -0,0 +3,2 @@\n+a\n+b\n"
	if got := ChangedCoverage(diff, lc); !slices.Equal(got.Uncovered, []string{"src/main/java/com/x/Foo.java:4"}) {
		t.Errorf("jacoco changed coverage = %+v", got)
	}
}

func TestParseLineCoverageHasNoDataForTheRest(t *testing.T) {
	for _, lang := range []string{"javascript", "typescript", "rust", "cobol"} {
		if _, ok := ParseLineCoverage(lang, "{\"total\":{\"lines\":{\"pct\":90}}}"); ok {
			t.Errorf("%s: line data from a summary", lang)
		}
	}
}

func TestCheckCoverageStepListsUncoveredAddedLines(t *testing.T) {
	cfg := CheckerConfig{CoverageFloor: 80, Diff: func(context.Context, string, string) (string, error) { return addedDiff, nil }}
	out := "total:\t(statements)\t90.0%\n" + goProfile
	sb := goSandbox().on("-coverprofile", reply{0, out})
	r, err := newTestChecker(cfg).Check(context.Background(), sb.run, goInput())
	if err != nil {
		t.Fatal(err)
	}
	if s := step(t, r, StepCoverage); s.Level != LevelPass {
		t.Errorf("coverage = %+v", s)
	}
	if !slices.Equal(r.Uncovered, []string{"a/a.go:4-5"}) {
		t.Errorf("uncovered = %v", r.Uncovered)
	}
}

func TestCheckCoverageStepFailsWhenNoAddedLineIsCovered(t *testing.T) {
	cfg := CheckerConfig{CoverageFloor: 80, Diff: func(context.Context, string, string) (string, error) { return addedDiff, nil }}
	out := "total:\t(statements)\t95.0%\nmode: set\nm/a/a.go:2.1,7.2 5 0\n"
	sb := goSandbox().on("-coverprofile", reply{0, out})
	r, err := newTestChecker(cfg).Check(context.Background(), sb.run, goInput())
	if err != nil {
		t.Fatal(err)
	}
	s := step(t, r, StepCoverage)
	if s.Level != LevelFail || !slices.Contains(s.Detail, "a/a.go:2-7") || r.Clean() {
		t.Errorf("coverage = %+v clean=%v", s, r.Clean())
	}
}

func TestCheckCoverageStepPassesWhenNothingExecutableWasAdded(t *testing.T) {
	diff := "+++ b/a/a.go\n@@ -1,1 +1,2 @@\n package a\n+// a comment\n"
	cfg := CheckerConfig{CoverageFloor: 80, Diff: func(context.Context, string, string) (string, error) { return diff, nil }}
	sb := goSandbox().on("-coverprofile", reply{0, "total:\t(statements)\t95.0%\n" + goProfile})
	r, err := newTestChecker(cfg).Check(context.Background(), sb.run, goInput())
	if err != nil || step(t, r, StepCoverage).Level != LevelPass || len(r.Uncovered) != 0 {
		t.Errorf("result: %+v %v", r, err)
	}
}
