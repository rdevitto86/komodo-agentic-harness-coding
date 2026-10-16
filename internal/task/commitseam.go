package task

import (
	"context"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/lease"
)

// SaveRequest names a finished task whose work Save records as a local commit.
type SaveRequest struct {
	Run      bus.ID      `json:"run"`
	Group    bus.ID      `json:"group"`
	Repo     string      `json:"repo"`     // repo id the lease is keyed by
	Label    string      `json:"label"`    // group label, such as tg-1.2
	Task     string      `json:"task"`     // finished task name
	Worktree string      `json:"worktree"` // path of the group's worktree
	Branch   string      `json:"branch"`   // group branch
	Fence    lease.Fence `json:"fence"`    // fence of the lease the Task Manager holds
}

// SaveResult is what Save returns.
type SaveResult struct {
	Head  string `json:"head"`  // commit id at the group branch tip after the save
	Tree  string `json:"tree"`  // tree hash of that commit
	Saved bool   `json:"saved"` // false when the tip already held the worktree's content
}

// AgentVerdict is one agent's approval and the tree it judged.
type AgentVerdict struct {
	Tree    string   `json:"tree"`
	Summary []string `json:"summary"`
}

// PRFacts are the facts one group adds to the pull request body.
type PRFacts struct {
	DiffStat     string   `json:"diff_stat"`
	TasksClosed  []string `json:"tasks_closed"`
	FindingCodes []string `json:"finding_codes"`
}

// Approval is what the Task Manager hands over when the Tester and Reviewer both approve.
type Approval struct {
	Run        bus.ID       `json:"run"`
	Group      bus.ID       `json:"group"`
	Repo       string       `json:"repo"`
	Epic       string       `json:"epic"`
	Label      string       `json:"label"`
	EpicGroups []string     `json:"epic_groups"`
	Goal       string       `json:"goal"`
	Worktree   string       `json:"worktree"`
	Branch     string       `json:"branch"`
	Fence      lease.Fence  `json:"fence"`
	Tree       string       `json:"tree"`
	Tester     AgentVerdict `json:"tester"`
	Reviewer   AgentVerdict `json:"reviewer"`
	Facts      PRFacts      `json:"facts"`
}

// Landed is what a finished Publish returns.
type Landed struct {
	Branch string `json:"branch"`
	Head   string `json:"head"`
	Last   bool   `json:"last"`
	PRURL  string `json:"pr_url"`
	PRNew  bool   `json:"pr_new"`
}

// Committer is the Commit Handler as the Task Manager sees it.
type Committer interface {
	// Save makes a local commit of one finished task; it never pushes.
	Save(ctx context.Context, r SaveRequest) (SaveResult, error)
	// Publish hands over the approved group; the Commit Handler lands it on its epic's branch.
	Publish(ctx context.Context, a Approval) (Landed, error)
}

// GroupMeta is what the Commit Handler needs about the group beyond the spec.
type GroupMeta struct {
	Repo       string   // repo id the lease is keyed by
	Epic       string   // epic label, such as ep-1
	EpicGroups []string // labels of every group of the epic
	Goal       string   // the group goal
}
