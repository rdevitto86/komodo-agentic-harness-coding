package agent

import (
	"context"
	"fmt"
)

// BaselineStore keeps baselines by the tree hash of their base commit.
type BaselineStore interface {
	// Load returns the stored baseline of a tree, if any group stored one.
	Load(ctx context.Context, tree string) (Baseline, bool, error)
	// Save stores a baseline beside the group's state.
	Save(ctx context.Context, b Baseline) error
}

// BaselineConfig supplies what producing a baseline reads besides Quality Assurance's sandbox runner.
type BaselineConfig struct {
	// stored baselines
	Store BaselineStore
	// tree hash of the base commit
	Tree func(ctx context.Context, base string) (string, error)
	// folder with the base commit's files, reachable from inside the sandbox
	Checkout func(ctx context.Context, base string) (string, error)
	// language of the repository
	Language string
	// shell command of the test class
	Test string
	// shell command that prints the JUnit XML report of a finished Test run; empty when none is known
	JUnit string
}

// NewBaselineSource returns the function that gives a base commit's baseline: the stored entry of its tree, else the
// test class run in Quality Assurance's sandbox on the base files. A run with no per-test results is not stored.
func NewBaselineSource(cfg BaselineConfig) func(context.Context, CommandRunner, string) (Baseline, error) {
	return func(ctx context.Context, run CommandRunner, base string) (Baseline, error) {
		tree, err := cfg.Tree(ctx, base)
		if err != nil {
			return Baseline{}, fmt.Errorf("read tree of the base: %w", err)
		}

		if b, ok, err := cfg.Store.Load(ctx, tree); err != nil || ok {
			return b, err
		}

		dir, err := cfg.Checkout(ctx, base)
		if err != nil {
			return Baseline{}, fmt.Errorf("check out the base: %w", err)
		}

		runs, err := cfg.baseRuns(ctx, run, dir)
		if err != nil {
			return Baseline{}, err
		}
		if len(runs) == 0 {
			return Baseline{TreeHash: tree, Passed: map[string]bool{}}, nil
		}

		b := NewBaseline(tree, runs)
		if err := cfg.Store.Save(ctx, b); err != nil {
			return Baseline{}, fmt.Errorf("store baseline: %w", err)
		}
		return b, nil
	}
}

// baseRuns runs the test class in the base checkout and returns one run per test; none when no report is available.
func (cfg BaselineConfig) baseRuns(ctx context.Context, run CommandRunner, dir string) ([]TestRun, error) {
	in := "cd " + shQuote(dir) + " && "

	if cfg.Language == "go" {
		_, out, err := run(ctx, in+"go test -json -count=1 ./...")
		if err != nil {
			return nil, fmt.Errorf("run base tests: %w", err)
		}
		return ParseGoTestJSON(out), nil
	}

	if _, _, err := run(ctx, in+cfg.Test); err != nil {
		return nil, fmt.Errorf("run base tests: %w", err)
	}
	if cfg.JUnit == "" {
		return nil, nil
	}

	_, rep, err := run(ctx, in+cfg.JUnit)
	if err != nil {
		return nil, fmt.Errorf("read base test report: %w", err)
	}

	// An unreadable report leaves no per-test results, which the caller treats like a missing one.
	runs, perr := ParseJUnit(rep)
	if perr != nil {
		return nil, nil
	}
	return runs, nil
}
