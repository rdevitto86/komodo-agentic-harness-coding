package agent

import (
	"context"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// pyInput is a Python group that changed one file.
func pyInput() CheckInput {
	return CheckInput{
		Group:    "g",
		Base:     "b",
		Head:     "h",
		Language: "python",
		Files:    []string{"a.py"},
		Changed:  []string{"a.py"},
	}
}

const junitMixed = `<?xml version="1.0"?><testsuites><testsuite name="p">
<testcase classname="tests.a" name="test_old"><failure message="boom">trace</failure></testcase>
<testcase classname="tests.a" name="test_n1"><failure message="x"/></testcase>
<testcase classname="tests.a" name="test_n2"><error message="y"/></testcase>
<testcase classname="tests.a" name="test_skip"><skipped/></testcase>
<testcase classname="tests.a" name="test_ok"/></testsuite></testsuites>`

func pyChecker(base Baseline, report bool) Checker {
	cfg := CheckerConfig{CoverageFloor: 80, TestCommands: map[string]string{"python": "pytest -q"},
		BaseResult: func(context.Context, string) (Baseline, error) { return base, nil }}
	if report {
		cfg.ReportCommands = map[string]string{"python": "cat junit.xml"}
	}
	return newTestChecker(cfg)
}

func pySandbox(xml string) *sandbox {
	sb := newSandbox().on("pytest -q", reply{
		1,
		"failed",
	}).on("coverage run", reply{0, "{\"totals\": {\"percent_covered\": 95}}\n"})
	if xml != "" {
		sb.on("cat junit.xml", reply{0, xml})
	}
	return sb
}

// Fix 1: a whole-command failure at base never hides a failure at head.
func TestWholeCommandFailureAtBaseNeverHidesHeadFailure(t *testing.T) {
	base := Baseline{Passed: map[string]bool{"pytest -q": false}}
	r, err := pyChecker(base, false).Check(context.Background(), pySandbox("").run, pyInput())
	if err != nil {
		t.Fatal(err)
	}
	s := step(t, r, StepBaseline)
	if s.Level != LevelFail || len(s.Detail) != 1 || !strings.Contains(s.Detail[0], "pytest -q") ||
		!strings.Contains(s.Detail[0], "cannot be classified") {
		t.Fatalf("baseline = %+v", s)
	}
}

// Fix 1: a JUnit report classifies each test against the baseline.
func TestJUnitReportClassifiesEachTest(t *testing.T) {
	base := Baseline{Passed: map[string]bool{"tests.a.test_old": false, "tests.a.test_ok": true}}
	r, err := pyChecker(base, true).Check(context.Background(), pySandbox(junitMixed).run, pyInput())
	if err != nil {
		t.Fatal(err)
	}
	if s := step(t, r, StepBaseline); s.Level != LevelFail ||
		!slices.Equal(s.Detail, []string{"tests.a.test_n1", "tests.a.test_n2"}) {
		t.Errorf("new failures = %+v", s)
	}
	only := `<testsuite><testcase classname="tests.a" name="test_old"><failure message="boom"/></testcase></testsuite>`
	r, err = pyChecker(base, true).Check(context.Background(), pySandbox(only).run, pyInput())
	if err != nil || !r.Clean() {
		t.Fatalf("only a pre-existing failure: %+v %v", r, err)
	}
	if s := step(t, r, StepBaseline); s.Level != LevelWarn || !slices.Equal(s.Detail, []string{"tests.a.test_old"}) {
		t.Errorf("pre-existing = %+v", s)
	}
}

// Fix 1: an unreadable report falls back to the whole command, which blocks.
func TestUnreadableReportFallsBackToWholeCommand(t *testing.T) {
	r, err := pyChecker(Baseline{}, true).Check(context.Background(), pySandbox("not xml").run, pyInput())
	if err != nil || step(t, r, StepBaseline).Level != LevelFail {
		t.Fatalf("result: %+v %v", r, err)
	}
}

func TestParseJUnit(t *testing.T) {
	runs, err := ParseJUnit("noise\n" + junitMixed)
	if err != nil || len(runs) != 5 {
		t.Fatalf("mixed: %d runs, %v", len(runs), err)
	}
	failed := 0
	for _, r := range runs {
		if !r.Passed {
			failed++
		}
		if r.Name == "test_old" && (r.Package != "tests.a" || !strings.Contains(r.Output, "trace")) {
			t.Errorf("old = %+v", r)
		}
	}
	if failed != 3 {
		t.Errorf("%d failed, want 3 (skipped passes)", failed)
	}
	if runs, err := ParseJUnit(`<testsuite><testcase classname="c" name="n"/></testsuite>`); err != nil || len(runs) != 1 ||
		!runs[0].Passed {
		t.Errorf("single suite: %+v %v", runs, err)
	}
	for _, bad := range []string{"", "plain text", "<testsuite></testsuite>", "<testsuite><testcase"} {
		if _, err := ParseJUnit(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

// Fix 2: non-Go files and nested modules still select tests.
func TestImpactSetMapsNonGoFiles(t *testing.T) {
	in := goInput()
	in.Files, in.Changed = []string{"a/testdata/x.golden"}, []string{"a/testdata/x.golden"}
	r, err := newTestChecker(CheckerConfig{
		CoverageFloor: 80,
	}).Check(context.Background(), goSandbox().on("-coverprofile", okCoverage("90")).run, in)
	if err != nil {
		t.Fatal(err)
	}
	if s := step(t, r, StepImpactSet); s.Level != LevelPass || !slices.Equal(s.Detail, []string{"m", "m/a", "m/b", "m/c"}) {
		t.Errorf("testdata impact = %+v", s)
	}
	in.Files, in.Changed = []string{"README.md"}, []string{"README.md"}
	r, _ = newTestChecker(CheckerConfig{
		CoverageFloor: 80,
	}).Check(context.Background(), goSandbox().on("-coverprofile", okCoverage("90")).run, in)
	if s := step(t, r, StepImpactSet); !slices.Equal(s.Detail, []string{"m"}) {
		t.Errorf("root file impact = %+v", s)
	}
}

func TestImpactSetRunsWholeModuleWhenUnmapped(t *testing.T) {
	in := goInput()
	in.Files, in.Changed = []string{"tools/go.mod", "tools/x.go"}, []string{"tools/go.mod", "tools/x.go"}
	sb := goSandbox().on("find . -name go.mod", reply{0, "./tools/go.mod\n"}).on("-coverprofile", okCoverage("90"))
	r, err := newTestChecker(CheckerConfig{CoverageFloor: 80}).Check(context.Background(), sb.run, in)
	if err != nil {
		t.Fatal(err)
	}
	s := step(t, r, StepImpactSet)
	if s.Level != LevelWarn || !slices.Contains(s.Detail, "m/c") ||
		!slices.ContainsFunc(s.Detail, func(d string) bool { return strings.HasPrefix(d, "whole module:") }) {
		t.Errorf("nested module impact = %+v", s)
	}
	noRoot := newSandbox().on("go list -m", reply{
		0,
		"m\n",
	}).on("go list -test", reply{0, "m/a\nm/b m/a\n"}).on("-coverprofile", okCoverage("90"))
	in.Files, in.Changed = []string{"docs/x.txt"}, []string{"docs/x.txt"}
	r, _ = newTestChecker(CheckerConfig{CoverageFloor: 80}).Check(context.Background(), noRoot.run, in)
	if s := step(t, r, StepImpactSet); s.Level != LevelWarn || !slices.Contains(s.Detail, "m/a") ||
		!slices.Contains(s.Detail, "m/b") {
		t.Errorf("unmapped impact = %+v", s)
	}
}

// Fix 3: a test that started and never finished counts as failed.
func TestUnfinishedTestCountsAsFailed(t *testing.T) {
	out := `{"Action":"run","Package":"m/a","Test":"TestKilled"}` + "\n" +
		`{"Action":"run","Package":"m/a","Test":"TestOK"}` + "\n" + `{"Action":"pass","Package":"m/a","Test":"TestOK"}` +
		"\n" +
		`{"Action":"output","Package":"m/b","Output":"panic: timed out\n"}` + "\n"
	got := map[string]bool{}
	for _, r := range ParseGoTestJSON(out) {
		got[r.Key()] = r.Passed
	}
	if passed, ok := got["m/a.TestKilled"]; !ok || passed {
		t.Errorf("unfinished test = %v, %v", passed, ok)
	}
	if !got["m/a.TestOK"] {
		t.Error("finished test failed")
	}
	if passed, ok := got["m/b."]; !ok || passed {
		t.Errorf("unfinished package = %v, %v", passed, ok)
	}
}

// Fix 4: the Tester answers every acceptance item exactly once.
func TestTesterAcceptResultChecksItems(t *testing.T) {
	tasks, items := []string{"t1"}, []string{"ac-1", "ac-2"}
	mk := func(ids ...string) TesterResult {
		r := TesterResult{Tasks: []TaskConfirmation{{Task: "t1", Confirmation: Confirmed}}}
		for _, id := range ids {
			r.Items = append(r.Items, ItemResult{Item: id, Pass: true, Evidence: "ok"})
		}
		return r
	}
	tr := NewTester()
	if err := tr.AcceptResult(tasks, items, mk("ac-2", "ac-1")); err != nil {
		t.Errorf("exact items: %v", err)
	}
	for name, r := range map[string]TesterResult{
		"missing":  mk("ac-1"),
		"repeated": mk("ac-1", "ac-2", "ac-1"),
		"unknown":  mk("ac-1", "ac-2", "ac-9"),
	} {
		if err := tr.AcceptResult(tasks, items, r); err == nil {
			t.Errorf("%s item accepted", name)
		}
	}
	if err := tr.AcceptResult(tasks, nil, mk()); err != nil {
		t.Errorf("group with no items: %v", err)
	}
}

// Fix 5: a slash-less file list entry matches only at the root.
func TestFileListEntryWithoutSlashMatchesOnlyAtRoot(t *testing.T) {
	in := goInput()
	in.Files, in.Changed = []string{"go.mod"}, []string{"go.mod", "tools/go.mod"}
	r, err := newTestChecker(CheckerConfig{
		CoverageFloor: 80,
	}).Check(context.Background(), goSandbox().on("-coverprofile", okCoverage("90")).run, in)
	if err != nil {
		t.Fatal(err)
	}
	if s := step(t, r, StepScope); s.Level != LevelFail || !slices.Equal(s.Detail, []string{"tools/go.mod"}) {
		t.Errorf("scope = %+v", s)
	}
}

func TestMatchPath(t *testing.T) {
	cases := []struct {
		glob, path string
		want       bool
	}{
		{"go.mod", "go.mod", true}, {"go.mod", "tools/go.mod", false}, {"*.md", "docs/a.md", false}, {"*.md", "a.md", true},
		{"docs/**", "docs/a/b.md", true},
		{"**/*.go", "a/b.go", true},
		{"a/b.go", "a/b.go", true},
		{"a/b.go", "c/a/b.go", false},
	}
	for _, c := range cases {
		if got := MatchPath(c.glob, c.path); got != c.want {
			t.Errorf("MatchPath(%q, %q) = %v", c.glob, c.path, got)
		}
	}
}

// Fix 6: a changed line that starts like a file header still counts.
func TestChangedLineThatLooksLikeAHeaderStillCounts(t *testing.T) {
	r := newTestReviewer(t)
	diff := "--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,1 @@\n-old\n+++ chan x\n"
	if got := r.Lenses(Diff{Paths: []string{"a.go"}, Text: diff}, ProfileThorough); !slices.Contains(got, "concurrency") {
		t.Errorf("lenses = %v", got)
	}
	if got := changedLines("--- a/a.go\n+++ b/a.go\n"); len(got) != 0 {
		t.Errorf("file headers counted: %v", got)
	}
}

// Fix 7: the last object that holds the coverage field is the report.
func TestCoverageReadsLastObjectThatHasTheField(t *testing.T) {
	got, err := ParseCoverage("python", "{\"totals\": {\"percent_covered\": 91.5}}\n{\"other\": 1}\n")
	if err != nil || got != 91.5 {
		t.Fatalf("got %v, %v", got, err)
	}
}

// Fix 8: no default report path for Java and Kotlin.
func TestJavaAndKotlinHaveNoDefaultCoverageCommand(t *testing.T) {
	for _, l := range []string{"java", "kotlin"} {
		if c, ok := CoverageCommand(l, "mvn test"); ok {
			t.Errorf("%s default = %q", l, c)
		}
	}
}

// Fix 9: a tiny cap never splits a character.
func TestCapOutputTinyCapKeepsCharacters(t *testing.T) {
	for _, max := range []int{3, 11, 21} {
		got := CapOutput(strings.Repeat("é", 50), max)
		if !utf8.ValidString(got) || len(got) > max {
			t.Errorf("max %d: %q (%d bytes)", max, got, len(got))
		}
	}
}
