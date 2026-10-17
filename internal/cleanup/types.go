// Package cleanup is the Cleanup Handler: it removes a published, abandoned or dead group's branch,
// worktree and Docker container.
package cleanup

import "errors"

// Published says that a group's work is on the remote and may be cleaned up.
type Published struct {
	Run      string `json:"run"`
	Group    string `json:"group"`
	Epic     string `json:"epic"`     // epic folder name, such as ep-1
	Branch   string `json:"branch"`   // group branch that was landed
	Tip      string `json:"tip"`      // commit id the remote must hold
	Remote   string `json:"remote"`   // remote that received the push
	Contains string `json:"contains"` // remote reference whose history must hold Tip
	Worktree string `json:"worktree"` // path of the group's checkout
	// path of the epic's landing worktree; used when EpicDone is true
	Landing string `json:"landing,omitempty"`
	// true when this was the epic's last group and its pull request is open
	EpicDone bool `json:"epic_done"`
	// epic pull request address; empty before the last group lands
	PRURL string `json:"pr_url"`
}

// Item is one thing the Cleanup Handler removed or kept.
type Item struct {
	Kind   string `json:"kind"`   // worktree, tester_worktree, container, branch, temp, version
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
	Testers   string // holds one tester checkout per group; empty means none exist
	Temp      string // holds one session temporary folder per group; empty means none exist
	Versions  string // holds one folder per installed binary version
}

// The kinds of Item.
const (
	KindWorktree  = "worktree"
	KindTester    = "tester_worktree"
	KindContainer = "container"
	KindBranch    = "branch"
	KindTemp      = "temp"
	KindVersion   = "version"
)

var (
	// Complete: nothing is removed
	ErrNotPublished = errors.New("remote does not hold the group's tip")
	// Sweep, Discard: the group is skipped
	ErrLeaseHeld      = errors.New("a live run holds the group")
	ErrUnknownVersion = errors.New("a binary version is unknown, so none can be deleted")
	ErrOutsideRoots   = errors.New("path is outside the folders cleanup may delete in")
)
