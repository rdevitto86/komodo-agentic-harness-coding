package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Rerun runs an item's command twice in the sandbox and returns the outcome and the last output; a run whose output passed
// the runner's limit counts by its exit code.
// An item with no command is never run; its failure stays unconfirmed.
func (goTester) Rerun(ctx context.Context, run CommandRunner, it Item) (RerunOutcome, string, error) {
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
