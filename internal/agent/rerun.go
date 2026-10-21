package agent

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Rerun runs an item's command twice in the sandbox and returns the outcome and the last output; a run whose output passed
// the runner's limit counts by its exit code.
// An item with no command is never run; its failure stays unconfirmed.
func (goQA) Rerun(ctx context.Context, run CommandRunner, it Item) (RerunOutcome, string, error) {
	if strings.TrimSpace(it.Cmd) == "" {
		return RerunUnconfirmed, "", nil
	}

	var passed [2]bool
	var last string

	for i := range passed {
		exit, out, err := run(ctx, it.Cmd)
		if err != nil && !errors.Is(err, ErrOutputOverLimit) {
			return "", "", fmt.Errorf("rerun item %s: %w", it.ID, err)
		}
		passed[i], last = exit == 0, out
	}

	return AdjudicateItem(passed), CapOutput(last, GateOutputCap), nil
}

// AdjudicateItem judges a failed item from Go's two reruns; true means the rerun passed.
func AdjudicateItem(reruns [2]bool) RerunOutcome {
	switch {
	case !reruns[0] && !reruns[1]:
		return RerunConfirmed
	case reruns[0] && reruns[1]:
		return RerunDisputed
	default:
		return RerunSplit
	}
}

// Replay runs each evidence run of a passed item again and returns the runs that did not reproduce, each with Go's exit
// code and output; a run whose command is neither the item's nor in the tool log is returned without being run (exit -1).
func (goQA) Replay(ctx context.Context, run CommandRunner, it Item, r ItemResult, logged []string) ([]EvidenceRun, error) {
	var bad []EvidenceRun

	for _, e := range r.Evidence {
		if e.Cmd != it.Cmd && !slices.Contains(logged, e.Cmd) {
			bad = append(bad, EvidenceRun{Cmd: e.Cmd, Exit: -1})
			continue
		}

		exit, out, err := run(ctx, e.Cmd)
		if err != nil && !errors.Is(err, ErrOutputOverLimit) {
			return nil, fmt.Errorf("replay item %s: %w", it.ID, err)
		}
		if exit != e.Exit || !hasLines(out, e.Excerpt) {
			bad = append(bad, EvidenceRun{Cmd: e.Cmd, Exit: exit, Excerpt: CapOutput(out, GateOutputCap)})
		}
	}

	return bad, nil
}

// volatileToken matches what changes between two runs of the same test: a duration such as 0.21s, (0.00s) or 12ms, and the
// (cached) mark go test prints for a result served from its cache. A cached result is Go's own record of an earlier run
// of the same inputs, so matching it is as sound as matching a fresh run.
var volatileToken = regexp.MustCompile(`\(cached\)|\d+(?:\.\d+)?(?:ms|s)\b`)

// NormalizeOutput replaces the durations and cached marks of go test output with one fixed token, so that two runs of the
// same test compare equal.
func NormalizeOutput(s string) string { return volatileToken.ReplaceAllString(s, "<time>") }

// hasLines reports whether every line of excerpt is a line of out once timings are normalised; the cut marker of a capped
// excerpt is not a line.
func hasLines(out, excerpt string) bool {
	have := map[string]bool{}
	for _, l := range strings.Split(NormalizeOutput(out), "\n") {
		have[strings.TrimSpace(l)] = true
	}

	for _, l := range strings.Split(NormalizeOutput(strings.ReplaceAll(excerpt, truncMarker, "\n")), "\n") {
		if l = strings.TrimSpace(l); l != "" && !have[l] {
			return false
		}
	}

	return true
}
