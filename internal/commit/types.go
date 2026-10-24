package commit

import (
	"errors"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/lease"
)

// SaveRequest names a finished task whose work Save records as a local commit.
type SaveRequest struct {
	Run      bus.ID      `json:"run"`
	Group    bus.ID      `json:"group"`
	Repo     string      `json:"repo"`     // repo id the lease is keyed by
	Label    string      `json:"label"`    // group label, such as tg-1.2
	Task     string      `json:"task"`     // finished task name, such as tsk-1.2.1
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

// Verdict is one agent's approval and the tree it judged.
type Verdict struct {
	Tree    string   `json:"tree"`    // tree hash of the work the agent judged
	Summary []string `json:"summary"` // result lines the pull request body prints
}

// Approval is what the Task Manager hands over when Quality Assurance and Reviewer both approve.
type Approval struct {
	Run        bus.ID      `json:"run"`
	Group      bus.ID      `json:"group"`
	Repo       string      `json:"repo"`        // repo id the lease is keyed by
	Epic       string      `json:"epic"`        // epic label, such as ep-1
	Label      string      `json:"label"`       // group label, such as tg-1.2
	EpicGroups []string    `json:"epic_groups"` // labels of every group of the epic
	Goal       string      `json:"goal"`        // the group goal; title of the commit
	Worktree   string      `json:"worktree"`    // path of the group's worktree
	Branch     string      `json:"branch"`      // group branch
	Fence      lease.Fence `json:"fence"`       // fence of the lease the Task Manager holds
	Tree       string      `json:"tree"`        // tree hash both agents approved
	QA         Verdict     `json:"qa"`
	Reviewer   Verdict     `json:"reviewer"`
	Facts      PRFacts     `json:"facts"`
}

// PRFacts are the facts one group adds to the pull request body; no model writes the text.
type PRFacts struct {
	DiffStat     string   `json:"diff_stat"`     // git's diff stat of the group
	TasksClosed  []string `json:"tasks_closed"`  // names of the closed tasks
	FindingCodes []string `json:"finding_codes"` // codes of findings that were resolved
}

// EpicFacts are the facts the epic pull request body renders.
type EpicFacts struct {
	// git's diff stat of the epic branch against the base branch
	DiffStat string `json:"diff_stat"`
	// one section per landed group, in landing order, as RenderBody wrote it
	Groups []string `json:"groups"`
}

// Result is what a finished Publish returns and what the Task Manager reports: the group landed on the local epic branch,
// and nothing was pushed.
type Result struct {
	Branch string `json:"branch"` // epic branch the group landed on
	Head   string `json:"head"`   // commit id at the local epic tip after the landing
	Landed string `json:"landed"` // the one commit that holds the group's work on the epic branch
	Last   bool   `json:"last"`   // true when this landing was the epic's last group
}

// EpicPush names the epic whose landed groups PushEpic pushes.
type EpicPush struct {
	Repo       string   `json:"repo"`        // repo id the epic's groups are keyed by
	Root       string   `json:"root"`        // the repository root
	CommonDir  string   `json:"common_dir"`  // the repository's git common directory
	Epic       string   `json:"epic"`        // the epic label, such as ep-1
	EpicGroups []string `json:"epic_groups"` // labels of every group of the epic
	// labels of the groups the Runtime Manager holds Done; the pull request opens only when this names every group
	Done []string `json:"done"`
}

// EpicResult is what PushEpic returns: what the epic branch holds locally and what reached the remote.
type EpicResult struct {
	Branch string   `json:"branch"` // epic branch
	Head   string   `json:"head"`   // commit id at the local epic tip
	Landed []string `json:"landed"` // labels of the groups landed on the local epic branch, in landing order
	Pushed bool     `json:"pushed"` // true when this call pushed the epic branch
	Last   bool     `json:"last"`   // true when every group of the epic has landed
	// epic pull request address; empty until every group has landed and the epic is not paused
	PRURL string `json:"pr_url"`
	PRNew bool   `json:"pr_new"` // true when this call opened the pull request
	// why the epic's cleanup failed after the push and pull request succeeded; empty when it succeeded
	CleanupError string `json:"cleanup_error,omitempty"`
}

// Published says that a group's work is one commit on the local epic branch and the group may be cleaned up.
type Published struct {
	Run      bus.ID `json:"run"`
	Group    bus.ID `json:"group"`
	Epic     string `json:"epic"`     // epic label, such as ep-1
	Branch   string `json:"branch"`   // group branch that was landed
	Tip      string `json:"tip"`      // commit id at the group branch tip
	Landed   string `json:"landed"`   // the commit on the epic branch that holds the same tree as Tip
	Into     string `json:"into"`     // local epic branch whose history must hold Landed
	Worktree string `json:"worktree"` // path of the group's checkout
}

// PublishedEpic says that an epic branch and its pull request are on the remote and the epic may be cleaned up.
type PublishedEpic struct {
	Epic     string `json:"epic"`     // epic label, such as ep-1
	Remote   string `json:"remote"`   // remote that received the push
	Contains string `json:"contains"` // remote reference whose history must hold the local epic tip
	PRURL    string `json:"pr_url"`   // epic pull request address
}

// Kinds of PublishError; the Runtime Manager maps each to a retry or a block reason.
const (
	KindNotApproved  = "not_approved"
	KindLeaseLost    = "lease_lost"
	KindCriticalRef  = "critical_ref"
	KindMergeConfl   = "merge_conflict"
	KindPushRejected = "push_rejected"
	KindPush         = "push"
	KindBaseMissing  = "base_missing"
	KindRecheck      = "recheck"
	KindForge        = "forge"
	KindGit          = "git"
	KindReport       = "report"
)

// PublishError says what failed and whether repeating the same call can fix it; Save and Publish
// both return it.
type PublishError struct {
	Kind      string // one of the Kind constants
	Retryable bool
	Err       error
}

var (
	// verdicts disagree, or the tree changed
	ErrNotApproved = errors.New("approval does not match the commit or the files")
	// fence higher, or the holder differs
	ErrLeaseLost = errors.New("lease lost: another run holds the group")
	ErrRecheck   = errors.New("checks failed on the epic branch after the landing")
)

// Error describes the failure with its kind.
func (e *PublishError) Error() string { return e.Kind + ": " + e.Err.Error() }

// Unwrap returns the underlying error so errors.Is finds the error values above.
func (e *PublishError) Unwrap() error { return e.Err }
