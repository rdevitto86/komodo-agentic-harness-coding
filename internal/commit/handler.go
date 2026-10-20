package commit

import (
	"context"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/lease"
)

// Settings are the values the Configuration Manager passes in.
type Settings struct {
	// commit.base_branch; empty means the remote's default branch
	BaseBranch string
	// commit.remote; empty means the base branch's upstream remote, origin when it has none
	Remote string
	// commit.pr_label; empty means komodo
	PRLabel string
	// folder that holds the epics' landing worktrees; empty means the folder that holds the group's
	// worktree
	LandingRoot string
}

// Leases reads a group's lease; the Lease Manager implements it and the Commit Handler never writes
// to it.
type Leases interface {
	Holder(ctx context.Context, repo, group string) (lease.Lease, bool, error)
}

// Reporter relays a progress fact to the Runtime Manager, which publishes it as a run-status event.
type Reporter interface {
	Report(ctx context.Context, group bus.ID, subtype string, data any) error
}

// Cleaner is the Cleanup Handler's entry for a landed group and for a pushed epic.
type Cleaner interface {
	Complete(ctx context.Context, p Published) error
	CompleteEpic(ctx context.Context, p PublishedEpic) error
}

// CheckResult is the outcome of one recheck.
type CheckResult struct {
	Pass   bool   `json:"pass"`
	Output string `json:"output"` // the exact output of the failing check; empty on a pass
}

// Rechecker runs the free checks on the epic branch checked out in a worktree and calls no model.
type Rechecker interface {
	// landed is true when the worktree's tip is the group's landing commit; before it the checks pass without running.
	Recheck(ctx context.Context, epic, worktree string, landed bool) (CheckResult, error)
}

// Handler saves and publishes work.
type Handler struct {
	Git     Git
	Forge   Forge
	Leases  Leases     // asks whether the lease still owns the group
	Epics   *EpicGates // one landing turn at a time per epic
	Check   Rechecker  // runs the free checks on the epic branch
	Report  Reporter   // the Task Manager, which relays to the Runtime Manager
	Cleanup Cleaner    // the Cleanup Handler
	Config  Settings   // values received from the Configuration Manager
	// Live, when set, is read at each use in place of Config, so a changed setting reaches the next call
	Live func() Settings
}

// settings returns the values to use now: Live's when set, otherwise Config.
func (h *Handler) settings() Settings {
	if h.Live != nil {
		return h.Live()
	}

	return h.Config
}
