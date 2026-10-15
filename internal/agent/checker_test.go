package agent

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// reply is one scripted sandbox answer.
type reply struct {
	exit int
	out  string
}

// sandbox is a fake command runner: each rule matches a command by substring and answers from a queue, the last answer repeating.
type sandbox struct {
	rules []*sbRule
	ran   []string
}

type sbRule struct {
	match   string
	replies []reply
	used    int
}

func newSandbox() *sandbox { return &sandbox{} }

// on adds a rule; rules are tried in order.
func (s *sandbox) on(match string, replies ...reply) *sandbox {
	s.rules = append(s.rules, &sbRule{match: match, replies: replies})
	return s
}

// run answers one command, or exit 0 with no output when no rule matches.
func (s *sandbox) run(_ context.Context, cmd string) (int, string, error) {
	s.ran = append(s.ran, cmd)
	for _, r := range s.rules {
		if strings.Contains(cmd, r.match) {
			i := min(r.used, len(r.replies)-1)
			r.used++
			return r.replies[i].exit, r.replies[i].out, nil
		}
	}
	return 0, "", nil
}

// count returns how many commands contained the substring.
func (s *sandbox) count(sub string) int {
	n := 0
	for _, c := range s.ran {
		if strings.Contains(c, sub) {
			n++
		}
	}
	return n
}

// jsonTest builds go test -json lines for one package's tests.
func jsonTest(pkg string, results map[string]bool) string {
	var b strings.Builder
	names := make([]string, 0, len(results))
	for n := range results {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		act := "pass"
		if !results[n] {
			act = "fail"
			fmt.Fprintf(&b, `{"Action":"output","Package":%q,"Test":%q,"Output":"--- FAIL: %s\n"}`+"\n", pkg, n, n)
		}
		fmt.Fprintf(&b, `{"Action":%q,"Package":%q,"Test":%q}`+"\n", act, pkg, n)
	}
	return b.String()
}

const goListOut = "m m/a m/b\nm/a\nm/b m/a\nm/b [m/b.test] m/a\nm/b.test m/b\nm/c m/b m/a\nm/b_test [m/b.test] m/b m/a\n"

// goInput is a Go group that changed package m/a.
func goInput() CheckInput {
	return CheckInput{Group: "tg-1", Base: "base1", Head: "tree1", Language: "go",
		Files: []string{"a/a.go"}, Changed: []string{"a/a.go", "a/a_test.go"}, DoneWhen: map[string]string{"tsk-a": "go test ./a/..."}}
}

// goSandbox answers the Go listing commands; callers add test and coverage rules before it.
func goSandbox() *sandbox {
	return newSandbox().on("go list -m", reply{0, "m\n"}).on("go list -test", reply{0, goListOut})
}

func okCoverage(pct string) reply {
	return reply{0, "ok\ntotal:\t(statements)\t" + pct + "%\n"}
}

func newTestChecker(cfg CheckerConfig) Checker {
	if cfg.Diff == nil {
		cfg.Diff = func(context.Context, string, string) (string, error) { return "", nil }
	}
	if cfg.SecretPatterns == nil {
		cfg.SecretPatterns = []*regexp.Regexp{regexp.MustCompile(`AKIA[0-9A-Z]{16}`)}
	}
	if cfg.Sibling == nil {
		cfg.Sibling = func(p string) bool { return strings.HasSuffix(p, "_test.go") }
	}
	return NewChecker(cfg)
}

func step(t *testing.T, r CheckResult, s Step) StepResult {
	t.Helper()
	for _, x := range r.Steps {
		if x.Step == s {
			return x
		}
	}
	t.Fatalf("no %s step in %+v", s, r.Steps)
	return StepResult{}
}

func TestCheckAllCleanRunsEveryStep(t *testing.T) {
	sb := goSandbox().on("go test -json", reply{0, jsonTest("m/a", map[string]bool{"TestA": true})}).on("-coverprofile", okCoverage("91.2"))
	r, err := newTestChecker(CheckerConfig{CoverageFloor: 80}).Check(context.Background(), sb.run, goInput())
	if err != nil || !r.Clean() || len(r.Steps) != 8 {
		t.Fatalf("clean run: %+v %v", r, err)
	}
	if got := step(t, r, StepImpactSet).Detail; !slices.Equal(got, []string{"m", "m/a", "m/b", "m/c"}) {
		t.Errorf("impact set = %v", got)
	}
	if sb.count("go test ./a/...") != 1 {
		t.Errorf("completion command ran %d times", sb.count("go test ./a/..."))
	}
}

func TestCheckFailingCompletionCommandReturnsExactOutput(t *testing.T) {
	sb := goSandbox().on("go test ./a/...", reply{1, "--- FAIL: TestLock\n    lock_test.go:9: want 1"}).on("-coverprofile", okCoverage("90"))
	r, err := newTestChecker(CheckerConfig{CoverageFloor: 80}).Check(context.Background(), sb.run, goInput())
	if err != nil || r.Clean() {
		t.Fatalf("result: %+v %v", r, err)
	}
	s := step(t, r, StepDoneWhen)
	if s.Level != LevelFail || !slices.Equal(s.Detail, []string{"tsk-a"}) || !strings.Contains(s.Output, "lock_test.go:9: want 1") {
		t.Errorf("done_when = %+v", s)
	}
}

func TestCheckScopeSecretsAndCaseCollision(t *testing.T) {
	diff := "diff --git a/a/a.go b/a/a.go\n--- a/a/a.go\n+++ b/a/a.go\n@@ -1,2 +1,4 @@\n package a\n+// ok\n+var key = \"AKIAABCDEFGHIJKLMNOP\"\n x\n"
	in := goInput()
	in.Changed = []string{"a/a.go", "docs/hld.md", "README.md", "readme.md"}
	sb := goSandbox().on("-coverprofile", okCoverage("90"))
	cfg := CheckerConfig{CoverageFloor: 80, Diff: func(context.Context, string, string) (string, error) { return diff, nil }}
	r, err := newTestChecker(cfg).Check(context.Background(), sb.run, in)
	if err != nil || r.Clean() {
		t.Fatalf("result: %+v %v", r, err)
	}
	if s := step(t, r, StepScope); s.Level != LevelFail || !slices.Equal(s.Detail, []string{"docs/hld.md", "README.md", "readme.md"}) {
		t.Errorf("scope = %+v", s)
	}
	s := step(t, r, StepSecrets)
	if s.Level != LevelFail || !slices.Equal(s.Detail, []string{"a/a.go:3"}) || strings.Contains(s.Output+strings.Join(s.Detail, ""), "AKIA") {
		t.Errorf("secrets = %+v", s)
	}
	if s := step(t, r, StepCaseCollision); s.Level != LevelFail || len(s.Detail) != 1 || !strings.Contains(s.Detail[0], "README.md") {
		t.Errorf("case_collision = %+v", s)
	}
}

func TestCheckRegressionBlocksPreExistingWarns(t *testing.T) {
	head := jsonTest("m/a", map[string]bool{"TestOld": false, "TestRegressed": false, "TestFine": true})
	sb := goSandbox().on("-run", reply{1, "FAIL"}).on("go test -json", reply{1, head}).on("-coverprofile", okCoverage("95"))
	base := Baseline{TreeHash: "t", Passed: map[string]bool{"m/a.TestOld": false, "m/a.TestRegressed": true, "m/a.TestFine": true}}
	cfg := CheckerConfig{CoverageFloor: 80, BaseResult: func(context.Context, string) (Baseline, error) { return base, nil }}
	r, err := newTestChecker(cfg).Check(context.Background(), sb.run, goInput())
	if err != nil || r.Clean() {
		t.Fatalf("result: %+v %v", r, err)
	}
	s := step(t, r, StepBaseline)
	if s.Level != LevelFail || !slices.Equal(s.Detail, []string{"m/a.TestRegressed"}) || !strings.Contains(s.Output, "FAIL: TestRegressed") {
		t.Errorf("baseline = %+v", s)
	}
	if sb.count("-coverprofile") != 0 {
		t.Error("coverage ran after a confirmed regression")
	}
	if !r.Done("tree1").Regression {
		t.Error("check.done does not flag the regression")
	}
}

func TestCheckPreExistingFailureOnlyWarns(t *testing.T) {
	head := jsonTest("m/a", map[string]bool{"TestOld": false})
	sb := goSandbox().on("-run", reply{1, "FAIL"}).on("go test -json", reply{1, head}).on("-coverprofile", okCoverage("95"))
	base := Baseline{Passed: map[string]bool{"m/a.TestOld": false}}
	cfg := CheckerConfig{CoverageFloor: 80, BaseResult: func(context.Context, string) (Baseline, error) { return base, nil }}
	r, err := newTestChecker(cfg).Check(context.Background(), sb.run, goInput())
	if err != nil || !r.Clean() {
		t.Fatalf("result: %+v %v", r, err)
	}
	if s := step(t, r, StepBaseline); s.Level != LevelWarn || !slices.Equal(s.Detail, []string{"m/a.TestOld"}) {
		t.Errorf("baseline = %+v", s)
	}
}

func TestCheckFlakyTestIsReportedAndDoesNotBlock(t *testing.T) {
	head := jsonTest("m/a", map[string]bool{"TestRace": false})
	sb := goSandbox().on("-run", reply{0, "ok"}).on("go test -json", reply{1, head}).on("-coverprofile", okCoverage("95"))
	r, err := newTestChecker(CheckerConfig{CoverageFloor: 80}).Check(context.Background(), sb.run, goInput())
	if err != nil || !r.Clean() {
		t.Fatalf("result: %+v %v", r, err)
	}
	if s := step(t, r, StepFlake); s.Level != LevelWarn || !slices.Equal(s.Detail, []string{"m/a.TestRace"}) || !slices.Equal(r.Flaky, []string{"m/a.TestRace"}) {
		t.Errorf("flake = %+v flaky %v", s, r.Flaky)
	}
	if sb.count("-run") != FlakeReruns || !strings.Contains(sb.ran[len(sb.ran)-3], "-count=1") {
		t.Errorf("solo reruns = %d: %v", sb.count("-run"), sb.ran)
	}
}

func TestCheckNewFailingTestBlocks(t *testing.T) {
	head := jsonTest("m/a", map[string]bool{"TestBrandNew": false})
	sb := goSandbox().on("-run", reply{1, "FAIL"}).on("go test -json", reply{1, head})
	r, err := newTestChecker(CheckerConfig{CoverageFloor: 80}).Check(context.Background(), sb.run, goInput())
	if err != nil || step(t, r, StepBaseline).Level != LevelFail {
		t.Fatalf("result: %+v %v", r, err)
	}
}

func TestCheckCoverageFloor(t *testing.T) {
	cases := []struct {
		name string
		pct  string
		base func(context.Context, string) (float64, bool, error)
		want Level
	}{
		{"79 under 80", "79.0", nil, LevelFail},
		{"80 on the floor", "80.0", nil, LevelPass},
		{"72 over a base of 70", "72", func(context.Context, string) (float64, bool, error) { return 70, true, nil }, LevelPass},
		{"68 under a base of 70", "68", func(context.Context, string) (float64, bool, error) { return 70, true, nil }, LevelFail},
	}
	for _, c := range cases {
		sb := goSandbox().on("-coverprofile", okCoverage(c.pct))
		r, err := newTestChecker(CheckerConfig{CoverageFloor: 80, BaseCoverage: c.base}).Check(context.Background(), sb.run, goInput())
		if err != nil || step(t, r, StepCoverage).Level != c.want {
			t.Errorf("%s: %+v %v", c.name, step(t, r, StepCoverage), err)
		}
	}
}

func TestCheckLanguageWithoutCoverageToolFailsNamingIt(t *testing.T) {
	in := CheckInput{Group: "g", Base: "b", Head: "h", Language: "cobol", Files: []string{"x.cob"}, Changed: []string{"x.cob"}}
	cfg := CheckerConfig{CoverageFloor: 80, TestCommands: map[string]string{"cobol": "run-tests"}}
	r, err := newTestChecker(cfg).Check(context.Background(), newSandbox().run, in)
	if err != nil {
		t.Fatal(err)
	}
	if s := step(t, r, StepCoverage); s.Level != LevelFail || !slices.Equal(s.Detail, []string{"cobol"}) {
		t.Errorf("coverage = %+v", s)
	}
}

func TestCheckNonGoRunsFullTestClass(t *testing.T) {
	in := CheckInput{Group: "g", Base: "b", Head: "h", Language: "python", Files: []string{"a.py"}, Changed: []string{"a.py"}}
	sb := newSandbox().on("pytest -q", reply{1, "1 failed"}).on("coverage run", reply{0, "...\n{\"totals\": {\"percent_covered\": 91.5}}\n"})
	cfg := CheckerConfig{CoverageFloor: 80, TestCommands: map[string]string{"python": "pytest -q"}}
	r, err := newTestChecker(cfg).Check(context.Background(), sb.run, in)
	if err != nil || step(t, r, StepBaseline).Level != LevelFail {
		t.Fatalf("result: %+v %v", r, err)
	}
	if _, err := newTestChecker(CheckerConfig{CoverageFloor: 80}).Check(context.Background(), newSandbox().run, in); err == nil {
		t.Error("a language with no test command ran")
	}
}

func TestCheckStepThatCannotRunReturnsError(t *testing.T) {
	boom := func(context.Context, string) (int, string, error) { return 0, "", fmt.Errorf("sandbox refused") }
	_, err := newTestChecker(CheckerConfig{CoverageFloor: 80}).Check(context.Background(), boom, goInput())
	if err == nil || !strings.Contains(err.Error(), "done_when") {
		t.Fatalf("error = %v", err)
	}
	cfg := CheckerConfig{CoverageFloor: 80, SecretPatterns: []*regexp.Regexp{}}
	cfg.Diff = func(context.Context, string, string) (string, error) { return "", nil }
	if _, err := NewChecker(cfg).Check(context.Background(), goSandbox().run, goInput()); err == nil || !strings.Contains(err.Error(), "secrets") {
		t.Fatalf("no patterns error = %v", err)
	}
}

func TestParseGoTestJSON(t *testing.T) {
	out := jsonTest("m/a", map[string]bool{"TestA": true, "TestB": false, "TestC/sub": false, "TestC": false})
	runs := ParseGoTestJSON(out)
	got := map[string]bool{}
	for _, r := range runs {
		got[r.Key()] = r.Passed
	}
	if len(got) != 3 || got["m/a.TestA"] != true || got["m/a.TestB"] != false || got["m/a.TestC/sub"] != false {
		t.Fatalf("runs = %v", got)
	}
	if _, parent := got["m/a.TestC"]; parent {
		t.Error("a parent test with a failing subtest is listed beside it")
	}
	build := `{"Action":"output","Package":"m/z","Output":"z.go:3: undefined: q\n"}` + "\n" + `{"Action":"fail","Package":"m/z"}` + "\n"
	if runs := ParseGoTestJSON(build); len(runs) != 1 || runs[0].Passed || runs[0].Key() != "m/z." || !strings.Contains(runs[0].Output, "undefined: q") {
		t.Fatalf("package failure = %+v", runs)
	}
	b := NewBaseline("t1", ParseGoTestJSON(out))
	if b.TreeHash != "t1" || !b.Passed["m/a.TestA"] || b.Passed["m/a.TestB"] {
		t.Errorf("baseline = %+v", b)
	}
}

func TestParseGoList(t *testing.T) {
	deps := parseGoList(goListOut)
	if _, ok := deps["m/b.test"]; ok {
		t.Error("generated test main kept")
	}
	if !slices.Contains(deps["m/b"], "m/a") || len(deps) != 4 {
		t.Errorf("deps = %v", deps)
	}
	if _, ok := deps["m/b_test"]; ok {
		t.Error("external test package kept apart from its package")
	}
}

func TestScanSecretsLocatesAddedLinesOnly(t *testing.T) {
	diff := "+++ b/x.go\n@@ -5,2 +5,3 @@\n-old AKIAABCDEFGHIJKLMNOP\n ctx\n+new AKIAABCDEFGHIJKLMNOP\n+safe\n+++ b/y.go\n@@ -0,0 +1 @@\n+AKIAABCDEFGHIJKLMNOP\n"
	got := scanSecrets(diff, []*regexp.Regexp{regexp.MustCompile(`AKIA[0-9A-Z]{16}`)})
	if !slices.Equal(got, []string{"x.go:6", "y.go:1"}) {
		t.Fatalf("locations = %v", got)
	}
}
