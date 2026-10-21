// Package cleanup is the Cleanup Handler: it removes a published, abandoned or dead group's branch,
// worktree and Docker container.
package cleanup

import "errors"

// Published says that a group's work is on the remote and may be cleaned up.
type Published struct {
	Run      string `json:"run"`
	Group    string `json:"group"`
	Epic     string `json:"epic"`              // epic folder name, such as ep-1
	Branch   string `json:"branch"`            // group branch that was landed
	Tip      string `json:"tip"`               // commit id the remote must hold
	Remote   string `json:"remote"`            // remote that received the push
	Contains string `json:"contains"`          // remote reference whose history must hold Tip
	Worktree string `json:"worktree"`          // path of the group's checkout
	Landing  string `json:"landing,omitempty"` // path of the epic's landing worktree; used when EpicDone is true
	EpicDone bool   `json:"epic_done"`         // true when this was the epic's last group and its pull request is open
	PRURL    string `json:"pr_url"`            // epic pull request address; empty before the last group lands
	// Landed is set when the group was squashed into the local epic branch and nothing was pushed: the commit there that
	// holds the group's work. The remote is not consulted and Into names the local epic branch that must hold that change.
	Landed string `json:"landed,omitempty"`
	Into   string `json:"into,omitempty"`
}

// PublishedEpic says that an epic's branch and pull request are on the remote, so its landing worktree and branch may go.
type PublishedEpic struct {
	Epic     string `json:"epic"`              // epic folder name, such as ep-1
	Remote   string `json:"remote"`            // remote that received the push
	Contains string `json:"contains"`          // remote reference whose history must hold the local epic tip
	Landing  string `json:"landing,omitempty"` // path of the epic's landing worktree
	PRURL    string `json:"pr_url"`            // epic pull request address
}

// Item is one thing the Cleanup Handler removed or kept.
type Item struct {
	Kind   string `json:"kind"`   // worktree, qa_worktree, container, branch, temp, version
	Name   string `json:"name"`   // path, branch, reference or version
	Reason string `json:"reason"` // why it was kept; empty for removed items
}

// Report lists what one call removed and what it kept, for the CLI to print.
type Report struct {
	Group   string `json:"group"` // empty for a Sweep
	Removed []Item `json:"removed"`
	Kept    []Item `json:"kept"`
}

// DiscardOptions are the inputs of Discard.
type DiscardOptions struct {
	Reason       string // recorded in the event
	KeepWorktree bool   // leave the checkout on disk for a person to inspect
}

// Roots are the folders the handler may delete inside; it refuses every path outside them.
type Roots struct {
	Worktrees string // holds one folder per group, such as ~/.komodo/wt/<repo-id>
	Landings  string // holds one landing worktree per epic
	QA        string // holds one Quality Assurance checkout per group; empty means none exist
	Temp      string // holds one session temporary folder per group; empty means none exist
	Versions  string // holds one folder per installed binary version
}

// The kinds of Item.
const (
	KindWorktree  = "worktree"
	KindQA        = "qa_worktree"
	KindContainer = "container"
	KindBranch    = "branch"
	KindTemp      = "temp"
	KindVersion   = "version"
)

var (
	ErrNotPublished   = errors.New("remote does not hold the group's tip") // Complete: nothing is removed
	ErrLeaseHeld      = errors.New("a live run holds the group")           // Sweep, Discard: the group is skipped
	ErrUnknownVersion = errors.New("a binary version is unknown, so none can be deleted")
	ErrOutsideRoots   = errors.New("path is outside the folders cleanup may delete in")
)
