package cli

import (
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/format"
)

// CleanData is the data of reply.clean.
type CleanData struct {
	Removed []CleanItem `json:"removed"` // what was removed or, with --dry-run, would be removed
}

// CleanItem is one thing clean removed.
type CleanItem struct {
	Kind string `json:"kind"` // worktree, branch, container, cache or log
	Name string `json:"name"`
}

// handleClean asks the engine to remove everything that belongs to no open run, or to list it with --dry-run.
func handleClean(c *Call) Result {
	req := c.request(engine.VerbClean, "")
	req.Flags = engine.Flags{DryRun: c.Global.DryRun}

	frames, res := c.ask(req)
	if res != nil {
		return *res
	}

	d := CleanData{Removed: []CleanItem{}}
	decodeFinal(frames, &d)
	rows := format.Table{Head: []string{"Kind", "Name"}, What: "items"}

	for _, it := range d.Removed {
		rows.Rows = append(rows.Rows, []string{it.Kind, it.Name})
	}
	return Result{
		Shape: "reply.clean",
		Facts: format.CleanFacts{Removed: rows},
		Data:  d,
	}
}
