package intake

import (
	"context"
	"fmt"
)

// baseCheck runs each task's done_when command once on the untouched base commit. A command that passes there proves
// nothing about the task, so it is a finding. The runner opens on first use and every result is kept for the whole plan.
type baseCheck struct {
	open   func(ctx context.Context) (BaseRunner, error)
	runner BaseRunner
	exits  map[string]int // exit code of each command already run
}

// findings returns one failing finding per task of the plan whose done_when command exits 0 on the base commit. A task with
// no done_when is skipped.
func (b *baseCheck) findings(ctx context.Context, p Blueprint) ([]Finding, error) {
	if b.open == nil {
		return nil, nil
	}

	var found []Finding

	for _, g := range p.Groups {
		for _, t := range g.Tasks {
			if t.DoneWhen == "" {
				continue
			}

			exit, err := b.exit(ctx, t.DoneWhen)
			if err != nil {
				return nil, err
			}
			if exit != 0 {
				continue
			}

			found = append(found, Finding{
				Rule:     RuleFailFirst,
				Severity: SeverityFail,
				Path:     "plan.md#" + g.Name + "/" + t.Name,
				Message: fmt.Sprintf(
					"task %s: done_when %q already passes on the untouched base commit; "+
						"name a command that fails until the change is made",
					t.Name, t.DoneWhen,
				),
			})
		}
	}

	return sortFindings(found), nil
}

// exit returns the exit code of a command on the base commit, running it only the first time it is asked for.
func (b *baseCheck) exit(ctx context.Context, command string) (int, error) {
	if code, ok := b.exits[command]; ok {
		return code, nil
	}
	if b.runner == nil {
		r, err := b.open(ctx)
		if err != nil {
			return 0, fmt.Errorf("open the base commit to check done_when: %w", err)
		}

		b.runner, b.exits = r, map[string]int{}
	}

	code, err := b.runner.Run(ctx, command)
	if err != nil {
		return 0, fmt.Errorf("run done_when %q on the base commit: %w", command, err)
	}

	b.exits[command] = code
	return code, nil
}

// close ends the runner, if one was opened.
func (b *baseCheck) close() {
	if b.runner != nil {
		_ = b.runner.Close()
	}
}
