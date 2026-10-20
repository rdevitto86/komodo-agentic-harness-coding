package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// lintInput is a Go group that changed one source file of package m/a and one test file.
func lintInput() CheckInput {
	in := goInput()
	in.Changed = []string{"a/a.go", "a/a_test.go"}
	return in
}

// lintChecker returns a checker whose base checkout is /base.
func lintChecker() Checker {
	return newTestChecker(CheckerConfig{
		CoverageFloor: 80,
		BaseDir:       func(context.Context, string) (string, error) { return "/base", nil },
	})
}

// lintSandbox answers the Go listing, the tests and the coverage run as passing; callers add build and lint rules after it.
func lintSandbox() *sandbox {
	return goSandbox().
		on("go test -json", reply{0, jsonTest("m/a", map[string]bool{"TestA": true})}).
		on("-coverprofile", okCoverage("91.2"))
}

func TestBuildStepFailsWithTheCommandOutput(t *testing.T) {
	sb := lintSandbox().on("go build ./...", reply{1, "a/a.go:3:2: undefined: x\n"})
	r, err := lintChecker().Check(context.Background(), sb.run, lintInput())
	if err != nil {
		t.Fatal(err)
	}

	s := step(t, r, StepBuild)
	if s.Level != LevelFail || !strings.Contains(s.Output, "undefined: x") || r.Clean() {
		t.Fatalf("build = %+v", s)
	}
	if l := step(t, r, StepLint); l.Level != LevelWarn || sb.count("golangci-lint run") != 0 {
		t.Errorf("lint after a failed build = %+v", l)
	}
}

func TestBuildStepPassesAndUsesTheLanguageBuild(t *testing.T) {
	sb := lintSandbox()
	r, err := lintChecker().Check(context.Background(), sb.run, lintInput())
	if err != nil {
		t.Fatal(err)
	}
	if s := step(t, r, StepBuild); s.Level != LevelPass || sb.count("go build ./...") != 1 || sb.count("make") != 0 {
		t.Errorf("build = %+v, ran %v", s, sb.ran)
	}
}

func TestBuildStepJudgesAnOverLimitOutputByItsExitCode(t *testing.T) {
	for exit, want := range map[int]Level{0: LevelPass, 2: LevelFail} {
		run := func(_ context.Context, cmd string) (int, string, error) {
			if strings.Contains(cmd, "go build") {
				return exit, "head\ntail", fmt.Errorf("%w: more than 16 bytes", ErrOutputOverLimit)
			}
			return 0, "m\n", nil
		}

		s, err := lintChecker().(checker).buildStep(context.Background(), run, lintInput())
		if err != nil || s.Level != want {
			t.Fatalf("exit %d: %+v, %v, want %v", exit, s, err, want)
		}
	}
}

func TestBuildStepRunnerErrorIsAnError(t *testing.T) {
	boom := func(context.Context, string) (int, string, error) { return -1, "", errors.New("sandbox refused") }
	if _, err := lintChecker().(checker).buildStep(context.Background(), boom, lintInput()); err == nil {
		t.Error("a runner error passed")
	}
}

func TestLintOnlyOldIssuesPasses(t *testing.T) {
	old := "a/a.go:10:2: Error return value is not checked (errcheck)\n1 issues:\n* errcheck: 1\n"
	sb := lintSandbox().on("cd '/base'", reply{1, old}).on("golangci-lint run", reply{1, old})
	r, err := lintChecker().Check(context.Background(), sb.run, lintInput())
	if err != nil {
		t.Fatal(err)
	}
	if s := step(t, r, StepLint); s.Level != LevelPass || len(s.Detail) != 0 || !r.Clean() {
		t.Fatalf("lint = %+v, steps %+v", s, r.Steps)
	}
}

func TestLintMovedLineIsNotNew(t *testing.T) {
	head := "a/a.go:42:2: Error return value is not checked (errcheck)\n"
	base := "a/a.go:10:2: Error return value is not checked (errcheck)\n"
	sb := lintSandbox().on("cd '/base'", reply{1, base}).on("golangci-lint run", reply{1, head})
	r, err := lintChecker().Check(context.Background(), sb.run, lintInput())
	if err != nil || step(t, r, StepLint).Level != LevelPass {
		t.Fatalf("lint = %+v, %v", step(t, r, StepLint), err)
	}
}

func TestLintNewIssueFailsAndNamesOnlyTheNewOne(t *testing.T) {
	old := "a/a.go:10:2: Error return value is not checked (errcheck)\n"
	head := old + "a/a.go:20:5: ineffectual assignment to x (ineffassign)\n"
	sb := lintSandbox().on("cd '/base'", reply{1, old}).on("golangci-lint run", reply{1, head})
	r, err := lintChecker().Check(context.Background(), sb.run, lintInput())
	if err != nil {
		t.Fatal(err)
	}

	s := step(t, r, StepLint)
	if s.Level != LevelFail || r.Clean() || len(s.Detail) != 1 || !strings.Contains(s.Detail[0], "ineffassign") {
		t.Fatalf("lint = %+v", s)
	}
	if !strings.Contains(s.Detail[0], "a/a.go:20") {
		t.Errorf("detail lacks the head location: %v", s.Detail)
	}
}

func TestLintSecondCopyOfAnOldIssueIsNew(t *testing.T) {
	line := "a/a.go:10:2: Error return value is not checked (errcheck)\n"
	sb := lintSandbox().on("cd '/base'", reply{1, line}).on("golangci-lint run", reply{1, line + line})
	r, err := lintChecker().Check(context.Background(), sb.run, lintInput())
	if err != nil || step(t, r, StepLint).Level != LevelFail {
		t.Fatalf("lint = %+v, %v", step(t, r, StepLint), err)
	}
}

func TestLintCleanHeadSkipsTheBaseRun(t *testing.T) {
	sb := lintSandbox()
	r, err := lintChecker().Check(context.Background(), sb.run, lintInput())
	if err != nil || step(t, r, StepLint).Level != LevelPass || sb.count("cd '/base'") != 0 {
		t.Fatalf("lint = %+v, %v, ran %v", step(t, r, StepLint), err, sb.ran)
	}
}

func TestLintWithoutBaseCheckoutCountsEveryIssueAsNew(t *testing.T) {
	cfg := CheckerConfig{CoverageFloor: 80}
	sb := lintSandbox().on("golangci-lint run", reply{1, "a/a.go:10:2: boom (errcheck)\n"})
	r, err := newTestChecker(cfg).Check(context.Background(), sb.run, lintInput())
	if err != nil || step(t, r, StepLint).Level != LevelFail {
		t.Fatalf("lint = %+v, %v", step(t, r, StepLint), err)
	}
}

func TestLintFailureWithNoReadableIssueFailsWithTheOutput(t *testing.T) {
	sb := lintSandbox().on("golangci-lint run", reply{3, "level=error msg=\"config is broken\"\n"})
	r, err := lintChecker().Check(context.Background(), sb.run, lintInput())
	if err != nil {
		t.Fatal(err)
	}
	if s := step(t, r, StepLint); s.Level != LevelFail || !strings.Contains(s.Output, "config is broken") {
		t.Fatalf("lint = %+v", s)
	}
}

func TestLintGoRunsOnlyTheChangedPackages(t *testing.T) {
	in := lintInput()
	in.Changed = append(in.Changed, "README.md")
	sb := lintSandbox()
	if _, err := lintChecker().Check(context.Background(), sb.run, in); err != nil {
		t.Fatal(err)
	}

	var script string
	for _, c := range sb.ran {
		if strings.Contains(c, "golangci-lint run") {
			script = c
		}
	}
	if !strings.Contains(script, "'./a'") || strings.Contains(script, "README") || strings.Contains(script, "./...") {
		t.Errorf("lint script = %q", script)
	}
}

func TestLintGoModChangeLintsTheWholeModule(t *testing.T) {
	in := lintInput()
	in.Changed = []string{"go.mod"}
	sb := lintSandbox()
	if _, err := lintChecker().Check(context.Background(), sb.run, in); err != nil {
		t.Fatal(err)
	}
	if sb.count("golangci-lint run --color never") == 0 || sb.count("'./...'") == 0 {
		t.Errorf("ran %v", sb.ran)
	}
}

func TestLintGoWithNoGoChangeRunsNothing(t *testing.T) {
	in := lintInput()
	in.Changed = []string{"README.md"}
	sb := lintSandbox()
	r, err := lintChecker().Check(context.Background(), sb.run, in)
	if err != nil || step(t, r, StepLint).Level != LevelPass || sb.count("golangci-lint run") != 0 {
		t.Fatalf("lint = %+v, %v, ran %v", step(t, r, StepLint), err, sb.ran)
	}
}

func TestLintGoFallsBackToVetWithoutGolangci(t *testing.T) {
	sb := lintSandbox().
		on("command -v golangci-lint", reply{1, ""}).
		on("cd '/base'", reply{0, ""}).
		on("go vet", reply{1, "# m/a\na/a.go:7:2: unreachable code\n"})
	r, err := lintChecker().Check(context.Background(), sb.run, lintInput())
	if err != nil {
		t.Fatal(err)
	}
	if s := step(t, r, StepLint); s.Level != LevelFail || sb.count("golangci-lint run") != 0 {
		t.Fatalf("lint = %+v, ran %v", s, sb.ran)
	}
}

func TestLintGoSkipsChangesInANestedModule(t *testing.T) {
	in := lintInput()
	in.Changed = []string{"tools/x/x.go"}
	sb := lintSandbox().on("find . -name go.mod", reply{0, "./tools/x/go.mod\n"})
	r, err := lintChecker().Check(context.Background(), sb.run, in)
	if err != nil || step(t, r, StepLint).Level != LevelPass || sb.count("golangci-lint run") != 0 {
		t.Fatalf("lint = %+v, %v, ran %v", step(t, r, StepLint), err, sb.ran)
	}
}

func TestBuildAndLintNonGoUseTheConfiguredCommands(t *testing.T) {
	in := CheckInput{
		Group:    "g",
		Base:     "b",
		Head:     "h",
		Language: "python",
		Files:    []string{"a.py"},
		Changed:  []string{"a.py"},
	}
	cfg := CheckerConfig{
		CoverageFloor:  80,
		TestCommands:   map[string]string{"python": "pytest -q"},
		BuildCommands:  map[string]string{"python": "python -m compileall ."},
		LintCommands:   map[string]string{"python": "ruff check"},
		BaseDir:        func(context.Context, string) (string, error) { return "/base", nil },
		ReportCommands: nil,
	}
	sb := newSandbox().
		on("coverage run", reply{0, "{\"totals\": {\"percent_covered\": 91.5}}\n"}).
		on("cd '/base'", reply{1, "a.py:1:1: F401 unused import\n"}).
		on("ruff check", reply{1, "a.py:1:1: F401 unused import\na.py:9:1: E501 line too long\n"})
	r, err := newTestChecker(cfg).Check(context.Background(), sb.run, in)
	if err != nil {
		t.Fatal(err)
	}
	if b := step(t, r, StepBuild); b.Level != LevelPass || sb.count("python -m compileall .") != 1 {
		t.Errorf("build = %+v", b)
	}
	if l := step(t, r, StepLint); l.Level != LevelFail || len(l.Detail) != 1 || !strings.Contains(l.Detail[0], "E501") {
		t.Errorf("lint = %+v", l)
	}
}

func TestBuildAndLintWithNoCommandAreSkippedWithAWarning(t *testing.T) {
	in := CheckInput{
		Group:    "g",
		Base:     "b",
		Head:     "h",
		Language: "java",
		Files:    []string{"A.java"},
		Changed:  []string{"A.java"},
	}
	cfg := CheckerConfig{CoverageFloor: 80, TestCommands: map[string]string{"java": "gradle test"}}
	r, err := newTestChecker(cfg).Check(context.Background(), newSandbox().run, in)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []Step{StepBuild, StepLint} {
		s := step(t, r, id)
		if s.Level != LevelWarn || len(s.Detail) != 1 || !strings.Contains(s.Detail[0], "java") {
			t.Errorf("%s = %+v", id, s)
		}
	}
}

func TestParseLintReadsFileRuleAndMessage(t *testing.T) {
	out := "# m/a\n" +
		"./a/a.go:12:3: Error return value is not checked (errcheck)\n" +
		"\tfoo()\n\t^\n" +
		"b.go:4: unreachable code\n" +
		"2 issues:\n* errcheck: 1\n"
	got := parseLint(out)
	want := []lintFinding{
		{File: "a/a.go", Line: "12", Rule: "errcheck", Message: "Error return value is not checked"},
		{File: "b.go", Line: "4", Message: "unreachable code"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("parseLint = %+v, want %+v", got, want)
	}
}

func TestNewLintFindingsCountsDuplicatesAndKeepsHeadOrder(t *testing.T) {
	a := lintFinding{File: "a.go", Line: "1", Rule: "r", Message: "m"}
	b := lintFinding{File: "b.go", Line: "2", Rule: "r", Message: "m"}
	moved := lintFinding{File: "a.go", Line: "99", Rule: "r", Message: "m"}
	got := newLintFindings([]lintFinding{b, moved, moved}, []lintFinding{a})
	if !slices.Equal(got, []lintFinding{b, moved}) {
		t.Errorf("new findings = %+v", got)
	}
}
