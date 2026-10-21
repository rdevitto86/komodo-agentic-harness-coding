package agent

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// DeepFuzzTime is how long each existing fuzz target runs.
	DeepFuzzTime = 30 * time.Second
	// BenchSlowdown is the slowdown over the base above which a benchmark warns.
	BenchSlowdown = 0.10
)

// DeepKind names one deep check.
type DeepKind string

const (
	DeepRace     DeepKind = "race"     // the language's race detector over the impact set
	DeepFuzz     DeepKind = "fuzz"     // random inputs against the changed functions
	DeepMutation DeepKind = "mutation" // deliberate edits to the changed functions
	DeepBench    DeepKind = "bench"    // the changed packages' benchmarks
)

// MutantStatus is what became of one mutant.
type MutantStatus string

const (
	MutantKilled   MutantStatus = "killed"   // a test failed
	MutantSurvived MutantStatus = "survived" // every test passed; Go records it
)

// Mutant is one deliberate edit to a changed line.
type Mutant struct {
	File   string       `json:"file"`
	Line   int          `json:"line"`
	Edit   string       `json:"edit"` // what changed, such as "< to <="
	Status MutantStatus `json:"status"`
}

// DeepResult is the outcome of one deep check; a warning is a pass with output.
type DeepResult struct {
	Kind    DeepKind `json:"kind"`
	Pass    bool     `json:"pass"`
	Output  string   `json:"output,omitempty"`  // exact command output, capped like a gate result
	Mutants []Mutant `json:"mutants,omitempty"` // mutation only
}

// Deep runs the deep checks of a plan on the work under test and returns one result per check.
type Deep interface {
	Run(ctx context.Context, run CommandRunner, plan []DeepKind) ([]DeepResult, error)
}

// DeepConfig supplies what the deep checks read besides the sandbox runner.
type DeepConfig struct {
	Language string                                                 // only "go" has deep checks yet
	Changed  func(ctx context.Context) ([]string, error)            // paths changed between the base and the work under test
	Base     string                                                 // the base commit the benchmarks compare with
	BaseDir  func(ctx context.Context, base string) (string, error) // folder with the base files; nil means no comparison
	FuzzTime time.Duration                                          // per fuzz target; DeepFuzzTime when zero
}

// deep runs the checks with a fixed configuration.
type deep struct{ cfg DeepConfig }

// NewDeep returns the Deep that runs the plan's checks in the sandbox with no model call.
func NewDeep(cfg DeepConfig) Deep {
	if cfg.FuzzTime <= 0 {
		cfg.FuzzTime = DeepFuzzTime
	}
	return deep{cfg}
}

// DeepPlan returns the deep checks that a risk level adds, in the order Go runs them.
func DeepPlan(l RiskLevel) []DeepKind {
	plan := []DeepKind{DeepRace}
	if l.Rank() >= RiskMedHigh.Rank() {
		plan = append(plan, DeepFuzz, DeepMutation, DeepBench)
	}
	return plan
}

// Run runs each check of the plan in order; an error means a check could not run, not that it failed.
func (d deep) Run(ctx context.Context, run CommandRunner, plan []DeepKind) ([]DeepResult, error) {
	if len(plan) == 0 {
		return nil, nil
	}

	var scope *deepScope

	if d.cfg.Language == "go" && slices.ContainsFunc(plan, func(k DeepKind) bool { return k != DeepMutation }) {
		var err error
		if scope, err = d.scope(ctx, run); err != nil {
			return nil, err
		}
	}

	var out []DeepResult

	for _, k := range plan {
		var (
			r   DeepResult
			err error
		)

		switch {
		case k == DeepMutation:
			r = DeepResult{Kind: k, Pass: true, Output: "skipped: mutation testing is not built, its tool is not chosen yet"}
		case scope == nil && d.cfg.Language == "go":
			err = fmt.Errorf("no scope for the %s check", k)
		case scope == nil:
			r = DeepResult{
				Kind:   k,
				Pass:   true,
				Output: fmt.Sprintf("skipped: no %s check for language %q", k, d.cfg.Language),
			}
		case k == DeepRace:
			r, err = d.race(ctx, run, scope)
		case k == DeepFuzz:
			r, err = d.fuzz(ctx, run, scope)
		case k == DeepBench:
			r, err = d.bench(ctx, run, scope)
		default:
			err = fmt.Errorf("unknown deep check %q", k)
		}
		if err != nil {
			return nil, fmt.Errorf("deep check %s: %w", k, err)
		}

		out = append(out, r)
	}

	return out, nil
}

// deepScope is the packages the checks look at.
type deepScope struct {
	impact  []string // changed packages and every package that imports one
	changed []string // packages that own a changed file
}

// scope lists the impact set and the changed packages of the work under test.
func (d deep) scope(ctx context.Context, run CommandRunner) (*deepScope, error) {
	files, err := d.cfg.Changed(ctx)
	if err != nil {
		return nil, fmt.Errorf("list changed files: %w", err)
	}

	impact, _, err := goImpactPackages(ctx, run, files)
	if err != nil {
		return nil, err
	}

	changed, err := changedPackages(ctx, run, files)
	if err != nil {
		return nil, err
	}

	return &deepScope{impact: impact, changed: changed}, nil
}

// changedPackages returns the packages that own a changed file, in order.
func changedPackages(ctx context.Context, run CommandRunner, files []string) ([]string, error) {
	_, out, err := runJudged(ctx, run, "go list -m")
	if err != nil {
		return nil, fmt.Errorf("read module path: %w", err)
	}

	module := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])

	_, out, err = runJudged(ctx, run, goListCmd)
	if err != nil {
		return nil, fmt.Errorf("list packages: %w", err)
	}

	deps := parseGoList(out)
	seen := map[string]bool{}

	var pkgs []string

	for _, f := range files {
		if p, ok := owningPackage(module, deps, f); ok && !seen[p] {
			seen[p] = true
			pkgs = append(pkgs, p)
		}
	}

	sort.Strings(pkgs)

	return pkgs, nil
}

// race runs the affected tests under the race detector.
func (d deep) race(ctx context.Context, run CommandRunner, s *deepScope) (DeepResult, error) {
	if len(s.impact) == 0 {
		return DeepResult{Kind: DeepRace, Pass: true, Output: "no affected package"}, nil
	}

	exit, out, err := runJudged(ctx, run, "go test -race -count=1 "+quoteAll(s.impact))
	if err != nil {
		return DeepResult{}, err
	}
	if exit != 0 {
		return DeepResult{Kind: DeepRace, Output: CapOutput(out, GateOutputCap)}, nil
	}

	return DeepResult{Kind: DeepRace, Pass: true}, nil
}

// fuzz runs each existing fuzz target of the changed packages for the fuzz time; a crash fails the check.
func (d deep) fuzz(ctx context.Context, run CommandRunner, s *deepScope) (DeepResult, error) {
	var crashes []string

	for _, pkg := range s.changed {
		_, out, err := runJudged(ctx, run, "go test -list '^Fuzz' "+shQuote(pkg))
		if err != nil {
			return DeepResult{}, err
		}

		for _, name := range fuzzTargets(out) {
			target := shQuote("^" + name + "$")
			cmd := fmt.Sprintf("go test -run '^$' -fuzz %s -fuzztime %s %s", target, d.cfg.FuzzTime, shQuote(pkg))

			exit, fout, err := runJudged(ctx, run, cmd)
			if err != nil {
				return DeepResult{}, err
			}
			if exit != 0 {
				crashes = append(crashes, fmt.Sprintf("%s %s:\n%s", pkg, name, CapOutput(fout, GateOutputCap)))
			}
		}
	}

	if len(crashes) > 0 {
		return DeepResult{Kind: DeepFuzz, Output: CapOutput(strings.Join(crashes, "\n"), GateOutputCap)}, nil
	}

	return DeepResult{Kind: DeepFuzz, Pass: true}, nil
}

// fuzzTargets returns the fuzz target names in the output of go test -list.
func fuzzTargets(out string) []string {
	var names []string

	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "Fuzz") && !strings.ContainsAny(l, " \t") {
			names = append(names, l)
		}
	}

	return names
}

// bench runs the changed packages' benchmarks and warns for each one more than BenchSlowdown slower than at the base.
func (d deep) bench(ctx context.Context, run CommandRunner, s *deepScope) (DeepResult, error) {
	res := DeepResult{Kind: DeepBench, Pass: true}
	if len(s.changed) == 0 || d.cfg.BaseDir == nil {
		return res, nil
	}

	cmd := "go test -run '^$' -bench . -benchtime 200ms -count=3 " + quoteAll(s.changed)

	_, head, err := runJudged(ctx, run, cmd)
	if err != nil {
		return DeepResult{}, err
	}

	dir, err := d.cfg.BaseDir(ctx, d.cfg.Base)
	if err != nil {
		return DeepResult{}, fmt.Errorf("check out the base: %w", err)
	}

	_, base, err := runJudged(ctx, run, "cd "+shQuote(dir)+" && "+cmd)
	if err != nil {
		return DeepResult{}, err
	}

	res.Output = CapOutput(slowdowns(parseBench(head), parseBench(base)), GateOutputCap)

	return res, nil
}

// benchLine matches one benchmark result line and captures its name and nanoseconds per operation.
var benchLine = regexp.MustCompile(`^(Benchmark\S*?)(?:-\d+)?\s+\d+\s+([0-9.]+) ns/op`)

// parseBench reads the fastest ns/op of each benchmark, keyed by package and name.
func parseBench(out string) map[string]float64 {
	best := map[string]float64{}
	pkg := ""

	for _, l := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(l, "pkg: "); ok {
			pkg = strings.TrimSpace(p)
			continue
		}

		m := benchLine.FindStringSubmatch(strings.TrimSpace(l))
		if m == nil {
			continue
		}

		ns, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			continue
		}
		if k := pkg + "." + m[1]; best[k] == 0 || ns < best[k] {
			best[k] = ns
		}
	}

	return best
}

// slowdowns lists the benchmarks present in both runs that are more than BenchSlowdown slower at head.
func slowdowns(head, base map[string]float64) string {
	var lines []string

	for k, h := range head {
		if b := base[k]; b > 0 && h > b*(1+BenchSlowdown) {
			lines = append(
				lines,
				fmt.Sprintf("%s: %.0f ns/op against %.0f ns/op at the base (+%.0f%%)", k, h, b, (h/b-1)*100),
			)
		}
	}

	sort.Strings(lines)

	return strings.Join(lines, "\n")
}

// quoteAll quotes each word and joins them with spaces.
func quoteAll(words []string) string {
	q := make([]string, len(words))
	for i, w := range words {
		q[i] = shQuote(w)
	}

	return strings.Join(q, " ")
}
