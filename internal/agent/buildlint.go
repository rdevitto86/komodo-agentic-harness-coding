package agent

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

const (
	goBuildCmd      = "go build ./..."
	lintDetailLimit = 20 // new findings a lint result names before it counts the rest
)

// lintLineRe reads one finding line: a file, a line, an optional column, the message and an optional trailing (rule).
var lintLineRe = regexp.MustCompile(`^(\S+?):(\d+)(?::\d+)?: (.+?)(?: \(([A-Za-z0-9_-]+)\))?$`)

// lintFinding is one lint problem; File, Rule and Message identify it, Line only locates it.
type lintFinding struct {
	File    string
	Line    string
	Rule    string
	Message string
}

// lintPlan is the head lint command and the function that turns it into the same command for the base checkout.
type lintPlan struct {
	head string
	base func(dir string) string
}

// buildStep runs the language's own build command and fails on a non-zero exit; a language without one is skipped.
func (c checker) buildStep(ctx context.Context, run CommandRunner, in CheckInput) (StepResult, error) {
	cmd, ok := c.cfg.BuildCommands[in.Language]
	if !ok && in.Language == "go" {
		cmd, ok = goBuildCmd, true
	}
	if !ok || cmd == "" {
		return skippedStep(StepBuild, "build", in.Language), nil
	}

	exit, out, err := runJudged(ctx, run, cmd)
	if err != nil {
		return StepResult{}, fmt.Errorf("run %s: %w", cmd, err)
	}

	s := StepResult{Step: StepBuild, Level: LevelPass}
	if exit != 0 {
		s.Level, s.Detail, s.Output = LevelFail, []string{cmd}, CapOutput(out, GateOutputCap)
	}
	return s, nil
}

// lintStep lints the changed code and fails only on findings the base commit lacks, matched by file, rule and message.
// It is skipped when the build fails or the language has no lint command.
func (c checker) lintStep(ctx context.Context, run CommandRunner, in CheckInput, buildFailed bool) (StepResult, error) {
	if buildFailed {
		return StepResult{Step: StepLint, Level: LevelWarn, Detail: []string{"skipped: the build fails"}}, nil
	}

	plan, ok, err := c.lintPlanFor(ctx, run, in)
	if err != nil {
		return StepResult{}, err
	}
	if !ok {
		return skippedStep(StepLint, "lint", in.Language), nil
	}

	s := StepResult{Step: StepLint, Level: LevelPass}
	if plan.head == "" {
		return s, nil
	}

	exit, out, err := runJudged(ctx, run, plan.head)
	if err != nil {
		return StepResult{}, fmt.Errorf("run lint: %w", err)
	}
	if exit == 0 {
		return s, nil
	}

	head := parseLint(out)
	if len(head) == 0 {
		s.Level, s.Output = LevelFail, CapOutput(out, GateOutputCap)
		return s, nil
	}

	base, err := c.baseFindings(ctx, run, in.Base, plan)
	if err != nil {
		return StepResult{}, err
	}

	if fresh := newLintFindings(head, base); len(fresh) > 0 {
		s.Level, s.Detail = LevelFail, lintDetail(fresh)
	}
	return s, nil
}

// lintPlanFor builds the lint command of the language; ok is false when it has none. An empty head means nothing to lint.
func (c checker) lintPlanFor(ctx context.Context, run CommandRunner, in CheckInput) (lintPlan, bool, error) {
	if in.Language != "go" {
		cmd := c.cfg.LintCommands[in.Language]
		if cmd == "" {
			return lintPlan{}, false, nil
		}
		return lintPlan{head: cmd, base: func(dir string) string { return "cd " + shQuote(dir) + " && " + cmd }}, true, nil
	}

	nested, err := nestedModules(ctx, run)
	if err != nil {
		return lintPlan{}, false, err
	}

	dirs, whole := goLintDirs(in.Changed, nested)
	if !whole && len(dirs) == 0 {
		return lintPlan{}, true, nil
	}

	exit, _, err := runJudged(ctx, run, golangciProbe())
	if err != nil {
		return lintPlan{}, false, fmt.Errorf("look for golangci-lint: %w", err)
	}

	tool := "go vet"
	if exit == 0 {
		tool = "golangci-lint run --color never --max-issues-per-linter 0 --max-same-issues 0"
	}

	return lintPlan{
		head: goLintScript("", tool, dirs, whole),
		base: func(dir string) string { return goLintScript("cd "+shQuote(dir)+" || exit 1; ", tool, dirs, whole) },
	}, true, nil
}

// baseFindings runs the lint command in the base checkout and returns what it reports; no checkout means no findings.
func (c checker) baseFindings(ctx context.Context, run CommandRunner, base string, plan lintPlan) ([]lintFinding, error) {
	if c.cfg.BaseDir == nil {
		return nil, nil
	}

	dir, err := c.cfg.BaseDir(ctx, base)
	if err != nil {
		return nil, fmt.Errorf("check out the base: %w", err)
	}

	_, out, err := runJudged(ctx, run, plan.base(dir))
	if err != nil {
		return nil, fmt.Errorf("run lint on the base: %w", err)
	}
	return parseLint(out), nil
}

// skippedStep returns the warning of a step the language has no command for.
func skippedStep(step Step, class, lang string) StepResult {
	return StepResult{
		Step:   step,
		Level:  LevelWarn,
		Detail: []string{fmt.Sprintf("skipped: no %s command for language %q", class, lang)},
	}
}

// runJudged runs a command and returns its exit code and output; an output past the runner's limit keeps both.
func runJudged(ctx context.Context, run CommandRunner, cmd string) (int, string, error) {
	exit, out, err := run(ctx, cmd)
	if err != nil && !errors.Is(err, ErrOutputOverLimit) {
		return exit, out, err
	}
	return exit, out, nil
}

// golangciProbe returns the command that exits 0 when golangci-lint is installed and the repository configures it.
func golangciProbe() string {
	var tests []string
	for _, f := range core.GolangciConfigs() {
		tests = append(tests, "[ -f "+shQuote(f)+" ]")
	}
	return "command -v golangci-lint >/dev/null 2>&1 && { " + strings.Join(tests, " || ") + "; }"
}

// goLintDirs returns the folders of the changed Go files outside nested modules; whole is true when go.mod or go.sum
// changed.
func goLintDirs(changed, nested []string) (dirs []string, whole bool) {
	for _, p := range changed {
		switch {
		case p == "go.mod" || p == "go.sum":
			whole = true
		case !strings.HasSuffix(p, ".go"):
		case slices.ContainsFunc(nested, func(d string) bool { return strings.HasPrefix(p, d+"/") }):
		default:
			dir := "."
			if d := path.Dir(p); d != "." {
				dir = "./" + d
			}
			if !slices.Contains(dirs, dir) {
				dirs = append(dirs, dir)
			}
		}
	}

	slices.Sort(dirs)
	return dirs, whole
}

// goLintScript returns the shell script that runs the tool on the given folders; it keeps only folders that still hold Go
// files and exits 0 when none do.
func goLintScript(prefix, tool string, dirs []string, whole bool) string {
	if whole {
		return prefix + tool + " './...'"
	}

	quoted := make([]string, len(dirs))
	for i, d := range dirs {
		quoted[i] = shQuote(d)
	}

	return prefix + "set --; for d in " + strings.Join(quoted, " ") +
		`; do ls "$d"/*.go >/dev/null 2>&1 && set -- "$@" "$d"; done; ` +
		`[ $# -gt 0 ] || exit 0; ` + tool + ` "$@"`
}

// parseLint reads the findings from a lint tool's text output; lines that are not a file:line finding are ignored.
func parseLint(out string) []lintFinding {
	var found []lintFinding
	for _, ln := range strings.Split(out, "\n") {
		m := lintLineRe.FindStringSubmatch(strings.TrimRight(ln, "\r"))
		if m == nil {
			continue
		}
		found = append(found, lintFinding{
			File:    strings.TrimPrefix(m[1], "./"),
			Line:    m[2],
			Rule:    m[4],
			Message: m[3],
		})
	}
	return found
}

// newLintFindings returns the head findings the base lacks, in head order; a finding counts once per copy, so a second
// copy of an old problem is new.
func newLintFindings(head, base []lintFinding) []lintFinding {
	key := func(f lintFinding) string { return f.File + "\x00" + f.Rule + "\x00" + f.Message }

	left := map[string]int{}
	for _, f := range base {
		left[key(f)]++
	}

	var fresh []lintFinding
	for _, f := range head {
		if k := key(f); left[k] > 0 {
			left[k]--
		} else {
			fresh = append(fresh, f)
		}
	}
	return fresh
}

// lintDetail renders new findings as file:line lines, naming the count of those past the limit.
func lintDetail(fresh []lintFinding) []string {
	var out []string
	for i, f := range fresh {
		if i == lintDetailLimit {
			out = append(out, fmt.Sprintf("%d more new findings", len(fresh)-lintDetailLimit))
			break
		}

		line := f.File + ":" + f.Line + ": " + f.Message
		if f.Rule != "" {
			line += " (" + f.Rule + ")"
		}
		out = append(out, line)
	}
	return out
}
