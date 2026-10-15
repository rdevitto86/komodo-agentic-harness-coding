package agent

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

// goListCmd prints each Go package with its dependencies, test variants included.
const goListCmd = `go list -test -f '{{.ImportPath}} {{join .Deps " "}}' ./...`

// CheckerConfig supplies what the steps read besides the sandbox runner.
type CheckerConfig struct {
	CoverageFloor    int                                                           // percent of lines tests must cover
	Diff             func(ctx context.Context, base, head string) (string, error)  // unified diff of the work under test
	BaseResult       func(ctx context.Context, base string) (Baseline, error)      // test-class result at the base; nil means an empty baseline
	BaseCoverage     func(ctx context.Context, base string) (float64, bool, error) // coverage at the base, when known
	SecretPatterns   []*regexp.Regexp                                              // content patterns of the guard's secret row
	Sibling          func(path string) bool                                        // reports a test sibling of a file in the list
	Match            func(glob, path string) bool                                  // file list matcher; MatchPath when nil
	TestCommands     map[string]string                                             // language to its test command class
	ReportCommands   map[string]string                                             // language to a command that prints its JUnit XML report
	CoverageCommands map[string]string                                             // language to a coverage command that replaces the default
}

// checker runs the mechanical checks with a fixed configuration.
type checker struct{ cfg CheckerConfig }

// NewChecker returns the Checker that runs the eight mechanical steps.
func NewChecker(cfg CheckerConfig) Checker {
	if cfg.Match == nil {
		cfg.Match = MatchPath
	}
	return checker{cfg}
}

// stepOrder is the order steps appear in a result.
var stepOrder = []Step{StepDoneWhen, StepScope, StepSecrets, StepCoverage, StepBaseline, StepImpactSet, StepFlake, StepCaseCollision}

// Check runs every step on the work under test; an error means a step could not run.
func (c checker) Check(ctx context.Context, run CommandRunner, in CheckInput) (CheckResult, error) {
	got := map[Step]StepResult{}
	got[StepCaseCollision] = caseStep(in.Changed)
	got[StepScope] = c.scopeStep(in)
	sec, err := c.secretsStep(ctx, in)
	if err != nil {
		return CheckResult{}, fmt.Errorf("step %s: %w", StepSecrets, err)
	}
	got[StepSecrets] = sec
	if got[StepDoneWhen], err = doneWhenStep(ctx, run, in); err != nil {
		return CheckResult{}, fmt.Errorf("step %s: %w", StepDoneWhen, err)
	}
	pkgs, notes, tests, solo, err := c.runImpact(ctx, run, in)
	if err != nil {
		return CheckResult{}, fmt.Errorf("step %s: %w", StepImpactSet, err)
	}
	got[StepImpactSet] = StepResult{Step: StepImpactSet, Level: LevelPass, Detail: pkgs}
	if len(notes) > 0 {
		got[StepImpactSet] = StepResult{Step: StepImpactSet, Level: LevelWarn, Detail: append(slices.Clone(pkgs), notes...)}
	}
	base, flake, flaky, blocked, err := c.judgeFailures(ctx, run, in, tests, solo)
	if err != nil {
		return CheckResult{}, fmt.Errorf("step %s: %w", StepBaseline, err)
	}
	got[StepBaseline], got[StepFlake] = base, flake
	if got[StepCoverage], err = c.coverageStep(ctx, run, in, blocked); err != nil {
		return CheckResult{}, fmt.Errorf("step %s: %w", StepCoverage, err)
	}
	res := CheckResult{Flaky: flaky}
	for _, s := range stepOrder {
		res.Steps = append(res.Steps, got[s])
	}
	return res, nil
}

// caseStep fails when two changed paths differ only by letter case.
func caseStep(changed []string) StepResult {
	s := StepResult{Step: StepCaseCollision, Level: LevelPass}
	for _, g := range CaseCollisions(changed) {
		s.Level = LevelFail
		s.Detail = append(s.Detail, strings.Join(g, ", "))
	}
	return s
}

// scopeStep fails when a changed file is neither in the file list nor a test sibling.
func (c checker) scopeStep(in CheckInput) StepResult {
	s := StepResult{Step: StepScope, Level: LevelPass}
	if out := OutOfScope(in.Changed, in.Files, c.cfg.Sibling, c.cfg.Match); len(out) > 0 {
		s.Level, s.Detail = LevelFail, out
	}
	return s
}

// secretsStep fails when an added line of the diff matches a secret pattern; it reports locations, never the line.
func (c checker) secretsStep(ctx context.Context, in CheckInput) (StepResult, error) {
	if c.cfg.Diff == nil || len(c.cfg.SecretPatterns) == 0 {
		return StepResult{}, errors.New("no diff source or secret patterns configured")
	}
	diff, err := c.cfg.Diff(ctx, in.Base, in.Head)
	if err != nil {
		return StepResult{}, fmt.Errorf("read diff: %w", err)
	}
	s := StepResult{Step: StepSecrets, Level: LevelPass}
	if hits := scanSecrets(diff, c.cfg.SecretPatterns); len(hits) > 0 {
		s.Level, s.Detail = LevelFail, hits
	}
	return s, nil
}

// doneWhenStep reruns every task's completion command and fails on any non-zero exit.
func doneWhenStep(ctx context.Context, run CommandRunner, in CheckInput) (StepResult, error) {
	s := StepResult{Step: StepDoneWhen, Level: LevelPass}
	ids := make([]string, 0, len(in.DoneWhen))
	for id := range in.DoneWhen {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var outs []string
	for _, id := range ids {
		exit, out, err := run(ctx, in.DoneWhen[id])
		if err != nil {
			return StepResult{}, fmt.Errorf("run completion command of %s: %w", id, err)
		}
		if exit != 0 {
			s.Level = LevelFail
			s.Detail = append(s.Detail, id)
			outs = append(outs, CapOutput(out, GateOutputCap))
		}
	}
	s.Output = joinOutputs(s.Detail, outs)
	return s, nil
}

// joinOutputs returns one output as it is, and several with the task or test name above each.
func joinOutputs(names, outs []string) string {
	if len(outs) == 1 {
		return outs[0]
	}
	var b strings.Builder
	for i, o := range outs {
		fmt.Fprintf(&b, "%s:\n%s\n", names[i], o)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// runImpact runs the tests the change affects and returns their results; solo gives the command that reruns one failure alone.
// notes name each change that maps to no package, for which the whole module ran.
func (c checker) runImpact(ctx context.Context, run CommandRunner, in CheckInput) (pkgs, notes []string, tests []TestRun, solo func(TestRun) string, err error) {
	if in.Language != "go" {
		tests, solo, err = c.runTestClass(ctx, run, in.Language)
		return []string{"test class"}, nil, tests, solo, err
	}
	pkgs, notes, err = goImpactPackages(ctx, run, in.Changed)
	if err != nil || len(pkgs) == 0 {
		return nil, nil, nil, nil, err
	}
	quoted := make([]string, len(pkgs))
	for i, p := range pkgs {
		quoted[i] = shQuote(p)
	}
	cmd := "go test -json -count=1 " + strings.Join(quoted, " ")
	exit, out, err := run(ctx, cmd)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("run affected tests: %w", err)
	}
	tests = ParseGoTestJSON(out)
	if len(tests) == 0 && exit != 0 {
		tests = []TestRun{{Name: "go test", Whole: true, Output: CapOutput(out, GateOutputCap)}}
	}
	return pkgs, notes, tests, func(t TestRun) string {
		if t.Package == "" {
			return cmd
		}
		return soloGoCommand(t)
	}, nil
}

// runTestClass runs a non-Go language's test command and reads per-test results from its JUnit report when one is configured.
// Without per-test results, a failing command is one whole-command failure that can never be classed as pre-existing.
func (c checker) runTestClass(ctx context.Context, run CommandRunner, lang string) ([]TestRun, func(TestRun) string, error) {
	cmd := c.cfg.TestCommands[lang]
	if cmd == "" {
		return nil, nil, fmt.Errorf("no test command for language %q", lang)
	}
	exit, out, err := run(ctx, cmd)
	if err != nil {
		return nil, nil, fmt.Errorf("run %s: %w", cmd, err)
	}
	var tests []TestRun
	if rc := c.cfg.ReportCommands[lang]; rc != "" {
		_, rep, rerr := run(ctx, rc)
		if rerr != nil {
			return nil, nil, fmt.Errorf("read test report: %w", rerr)
		}
		if runs, perr := ParseJUnit(rep); perr == nil {
			tests = runs
		}
	}
	if tests == nil || (exit != 0 && !slices.ContainsFunc(tests, func(t TestRun) bool { return !t.Passed })) {
		tests = append(tests, TestRun{Name: cmd, Passed: exit == 0, Whole: true, Output: CapOutput(out, GateOutputCap)})
	}
	return tests, func(TestRun) string { return cmd }, nil
}

// goImpactPackages lists the packages a change affects: the packages owning the changed files and everything that imports one.
// A change that maps to no package, or lies in a nested module, selects the whole module and adds a note.
func goImpactPackages(ctx context.Context, run CommandRunner, changed []string) (pkgs, notes []string, err error) {
	exit, out, err := run(ctx, "go list -m")
	if err != nil || exit != 0 {
		return nil, nil, fmt.Errorf("read module path: exit %d: %w", exit, errors.Join(err, errors.New(CapOutput(out, 512))))
	}
	module := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
	exit, out, err = run(ctx, goListCmd)
	if err != nil || exit != 0 {
		return nil, nil, fmt.Errorf("list packages: exit %d: %w", exit, errors.Join(err, errors.New(CapOutput(out, 512))))
	}
	deps := parseGoList(out)
	nested, err := nestedModules(ctx, run)
	if err != nil {
		return nil, nil, err
	}
	var hit []string
	whole := false
	for _, p := range changed {
		switch {
		case p == "go.mod" || p == "go.sum":
			whole = true
		case slices.ContainsFunc(nested, func(d string) bool { return strings.HasPrefix(p, d+"/") }):
			whole = true
			notes = append(notes, "whole module: "+p+" is in a nested module")
		default:
			if pkg, ok := owningPackage(module, deps, p); ok {
				hit = append(hit, pkg)
			} else {
				whole = true
				notes = append(notes, "whole module: "+p+" belongs to no package")
			}
		}
	}
	if whole {
		hit = keysOf(deps)
	}
	return ImpactSet(deps, hit), notes, nil
}

// nestedModules returns the folders of go.mod files below the root module.
func nestedModules(ctx context.Context, run CommandRunner) ([]string, error) {
	exit, out, err := run(ctx, "find . -name go.mod -not -path ./go.mod -not -path '*/node_modules/*'")
	if err != nil || exit != 0 {
		return nil, fmt.Errorf("list nested modules: exit %d: %w", exit, errors.Join(err, errors.New(CapOutput(out, 512))))
	}
	var dirs []string
	for _, ln := range strings.Split(out, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			dirs = append(dirs, strings.TrimPrefix(path.Dir(ln), "./"))
		}
	}
	return dirs, nil
}

// owningPackage returns the nearest package at or above a changed file's folder.
func owningPackage(module string, deps map[string][]string, file string) (string, bool) {
	for dir := path.Dir(file); ; dir = path.Dir(dir) {
		pkg := module
		if dir != "." {
			pkg = module + "/" + dir
		}
		if _, ok := deps[pkg]; ok {
			return pkg, true
		}
		if dir == "." {
			return "", false
		}
	}
}

// keysOf returns the keys of a map.
func keysOf(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// judgeFailures reruns each failing test twice alone and classifies the confirmed ones against the baseline.
// blocked is true when a regression or new failure was confirmed.
func (c checker) judgeFailures(ctx context.Context, run CommandRunner, in CheckInput, tests []TestRun, solo func(TestRun) string) (base, flake StepResult, flaky []string, blocked bool, err error) {
	base = StepResult{Step: StepBaseline, Level: LevelPass}
	flake = StepResult{Step: StepFlake, Level: LevelPass}
	var bl Baseline
	loaded := false
	var blockNames, blockOuts, warnNames []string
	for _, t := range tests {
		if t.Passed {
			continue
		}
		var passed [2]bool
		for i := range passed {
			exit, _, rerr := run(ctx, solo(t))
			if rerr != nil {
				return base, flake, nil, false, fmt.Errorf("rerun %s: %w", t.Key(), rerr)
			}
			passed[i] = exit == 0
		}
		if ClassifyFailure(passed) == FlakeFlaky {
			flaky = append(flaky, t.Key())
			continue
		}
		if t.Whole {
			blockNames = append(blockNames, t.Name+": per-test results are unavailable, so the failure cannot be classified against the baseline")
			blockOuts = append(blockOuts, t.Output)
			continue
		}
		if !loaded {
			loaded = true
			if c.cfg.BaseResult != nil {
				if bl, err = c.cfg.BaseResult(ctx, in.Base); err != nil {
					return base, flake, nil, false, fmt.Errorf("read baseline: %w", err)
				}
			}
		}
		if bl.Classify(t.Key(), false) == StatusPreExisting {
			warnNames = append(warnNames, t.Key())
			continue
		}
		blockNames = append(blockNames, t.Key())
		blockOuts = append(blockOuts, t.Output)
	}
	switch {
	case len(blockNames) > 0:
		base.Level, base.Detail, base.Output = LevelFail, blockNames, joinOutputs(blockNames, blockOuts)
	case len(warnNames) > 0:
		base.Level, base.Detail = LevelWarn, warnNames
	}
	if len(flaky) > 0 {
		flake.Level, flake.Detail = LevelWarn, flaky
	}
	return base, flake, flaky, len(blockNames) > 0, nil
}

// coverageStep runs the coverage command last, unless a confirmed regression already blocks the group.
func (c checker) coverageStep(ctx context.Context, run CommandRunner, in CheckInput, blocked bool) (StepResult, error) {
	s := StepResult{Step: StepCoverage, Level: LevelPass}
	if blocked {
		s.Detail = []string{"skipped: a regression is confirmed"}
		return s, nil
	}
	cmd, ok := c.cfg.CoverageCommands[in.Language]
	if !ok {
		cmd, ok = CoverageCommand(in.Language, c.cfg.TestCommands[in.Language])
	}
	if !ok {
		s.Level, s.Detail = LevelFail, []string{in.Language}
		return s, nil
	}
	exit, out, err := run(ctx, cmd)
	if err != nil {
		return StepResult{}, fmt.Errorf("run coverage command: %w", err)
	}
	head, err := ParseCoverage(in.Language, out)
	if err != nil {
		if exit != 0 {
			s.Level, s.Output = LevelFail, CapOutput(out, GateOutputCap)
			return s, nil
		}
		return StepResult{}, err
	}
	var baseCov float64
	var haveBase bool
	if head < float64(c.cfg.CoverageFloor) && c.cfg.BaseCoverage != nil {
		if baseCov, haveBase, err = c.cfg.BaseCoverage(ctx, in.Base); err != nil {
			return StepResult{}, fmt.Errorf("read base coverage: %w", err)
		}
	}
	if s.Level = CoverageLevel(c.cfg.CoverageFloor, baseCov, haveBase, head); s.Level == LevelFail {
		s.Detail = []string{fmt.Sprintf("%.1f%% is under the floor of %d%%", head, c.cfg.CoverageFloor)}
	}
	return s, nil
}
