package commit

import (
	"context"
	"errors"
)

// Git runs every git write of the harness; it has no force option and no refspec argument.
type Git interface {
	// TreeOf returns the tree hash of the worktree's content, tracked and untracked, ignored files skipped.
	TreeOf(ctx context.Context, worktree string) (string, error)
	// Head returns the commit id and tree hash of the worktree's checked-out commit.
	Head(ctx context.Context, worktree string) (commit, tree string, err error)
	// Info describes one commit of the worktree: its id, tree, first parent and subject.
	Info(ctx context.Context, worktree, rev string) (CommitInfo, error)
	// Commit records the worktree's content as one commit on the checked-out branch and returns its id.
	Commit(ctx context.Context, worktree, message string) (commit string, err error)
	// Push pushes one komodo/ branch to its own name on the remote, with a plain push.
	Push(ctx context.Context, worktree, remote, branch string) error
	// RemoteTip returns the commit id the remote holds for a branch; the bool is false when it has none.
	RemoteTip(ctx context.Context, worktree, remote, branch string) (string, bool, error)
	// RemoveWorktree removes a group's worktree and refuses when it holds files the tip lacks.
	RemoveWorktree(ctx context.Context, clone, path string) error
	// DeleteBranch deletes a local komodo/ branch only when it still points at expectTip and that commit is in the history of containedIn.
	DeleteBranch(ctx context.Context, clone, branch, expectTip, containedIn string) error
	// Merge merges ref into the branch checked out in dir with the given message; it never rebases, and a conflict aborts the merge.
	Merge(ctx context.Context, dir, ref, message string, noFF bool) (head string, err error)
	// AddWorktree adds a worktree at path on a komodo/ branch cut from base, creating the branch when it does not exist.
	AddWorktree(ctx context.Context, clone, path, branch, base string) error
	// Fetch fetches one branch of the remote into its remote-tracking reference.
	Fetch(ctx context.Context, dir, remote, branch string) error
	// Landed lists the group landings on head since base, newest first, read from the merge commits on its first-parent line.
	Landed(ctx context.Context, dir, base, head string) ([]Landing, error)
	// DiffStat returns git's diff stat of head against base.
	DiffStat(ctx context.Context, dir, base, head string) (string, error)
	// DiscardBranch deletes a local komodo/ branch that still points at expectTip, whether or not the remote holds that commit.
	DiscardBranch(ctx context.Context, clone, branch, expectTip string) error
	// BranchExists reports whether a komodo/ branch exists locally or among the remote-tracking references.
	BranchExists(ctx context.Context, clone, branch string) (bool, error)
	// CreateBranch cuts a komodo/ branch from base without checking it out.
	CreateBranch(ctx context.Context, clone, branch, base string) error
	// DefaultBranch returns the branch the remote's HEAD names, such as main.
	DefaultBranch(ctx context.Context, dir, remote string) (string, error)
	// UpstreamRemote returns the remote the branch tracks; the bool is false when it tracks none.
	UpstreamRemote(ctx context.Context, dir, branch string) (string, bool, error)
}

// CommitInfo describes one commit.
type CommitInfo struct {
	ID      string `json:"id"`      // commit id
	Tree    string `json:"tree"`    // tree hash of the commit
	Parent  string `json:"parent"`  // first parent's id; empty for a root commit
	Subject string `json:"subject"` // first line of the message
}

// Landing is one group landing on an epic branch, read back from its merge commit.
type Landing struct {
	Label   string   `json:"label"`   // group label, from the subject
	Goal    string   `json:"goal"`    // group goal, from the subject
	Section string   `json:"section"` // the group's section of the pull request body, from the message body
	Files   []string `json:"files"`   // files the landing changed
}

var (
	ErrTrailer       = errors.New("commit message ends in trailer lines")
	ErrDirty         = errors.New("worktree holds files the tip lacks")
	ErrTipMoved      = errors.New("branch no longer points at the expected commit")
	ErrNotContained  = errors.New("commit is not in the history of the other reference")
	ErrBranchMissing = errors.New("remote has no such branch")
	ErrNoGitDir      = errors.New("worktree is not registered with a known clone")
	ErrForce         = errors.New("git arguments would force or delete a remote reference")
)
