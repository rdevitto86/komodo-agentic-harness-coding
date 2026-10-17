package task

import (
	"context"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/commit"
)

// Committer is the Commit Handler as the Task Manager sees it.
type Committer interface {
	// Save makes a local commit of one finished task; it never pushes.
	Save(ctx context.Context, r commit.SaveRequest) (commit.SaveResult, error)
	// Publish hands over the approved group; the Commit Handler lands it on its epic's branch.
	Publish(ctx context.Context, a commit.Approval) (commit.Result, error)
}

// GroupMeta is what the Commit Handler needs about the group beyond the spec.
type GroupMeta struct {
	Repo       string   // repo id the lease is keyed by
	Epic       string   // epic label, such as ep-1
	EpicGroups []string // labels of every group of the epic
	Goal       string   // the group goal
}

var _ Committer = (*commit.Handler)(nil)
