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

// Approval is what the Task Manager hands over when the Tester and Reviewer both approve.
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
	Tester     Verdict     `json:"tester"`
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
	DiffStat string   `json:"diff_stat"` // git's diff stat of the epic branch against the base branch
	Groups   []string `json:"groups"`    // one section per landed group, in landing order, as RenderBody wrote it
}

// Result is what a finished Publish returns and what the Task Manager reports.
type Result struct {
	Branch string `json:"branch"` // epic branch that was pushed
	Head   string `json:"head"`   // commit id at the pushed epic tip
	Last   bool   `json:"last"`   // true when this landing was the epic's last group
	PRURL  string `json:"pr_url"` // epic pull request address; empty until the last group has landed
	PRNew  bool   `json:"pr_new"` // true when this call opened the pull request
}

// ListsRequest names the epic whose task lists SaveLists commits.
type ListsRequest struct {
	Repo      string `json:"repo"`       // repo id the lease is keyed by
	Root      string `json:"root"`       // the repository root, where docs/backlog/ is read
	CommonDir string `json:"common_dir"` // the repository's git common directory
	Epic      string `json:"epic"`       // the epic label, such as ep-1
}

// Published says that a group's work is on the remote and may be cleaned up.
type Published struct {
	Run      bus.ID `json:"run"`
	Group    bus.ID `json:"group"`
	Epic     string `json:"epic"`      // epic label, such as ep-1
	Branch   string `json:"branch"`    // group branch that was landed
	Tip      string `json:"tip"`       // commit id the remote must hold
	Remote   string `json:"remote"`    // remote that received the push
	Contains string `json:"contains"`  // remote reference whose history must hold Tip
	Worktree string `json:"worktree"`  // path of the group's checkout
	EpicDone bool   `json:"epic_done"` // true when this was the epic's last group and its pull request is open
	PRURL    string `json:"pr_url"`    // epic pull request address; empty before the last group lands
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

// PublishError says what failed and whether repeating the same call can fix it; Save and Publish both return it.
type PublishError struct {
	Kind      string // one of the Kind constants
	Retryable bool
	Err       error
}

// Error describes the failure with its kind.
func (e *PublishError) Error() string { return e.Kind + ": " + e.Err.Error() }

// Unwrap returns the underlying error so errors.Is finds the error values below.
func (e *PublishError) Unwrap() error { return e.Err }

var (
	ErrNotApproved  = errors.New("approval does not match the commit or the files") // verdicts disagree, or the tree changed
	ErrLeaseLost    = errors.New("lease lost: another run holds the group")          // fence higher, or the holder differs
	ErrCriticalRef  = errors.New("reference is outside refs/heads/komodo/ and refs/komodo/")
	ErrConflict     = errors.New("merge conflict")
	ErrPushRejected = errors.New("remote rejected the push")
	ErrRecheck      = errors.New("checks failed on the epic branch after the landing")
)
