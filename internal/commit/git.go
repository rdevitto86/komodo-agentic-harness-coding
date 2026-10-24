package commit

import (
	"context"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// Git is the part of the git wrapper the Commit Handler uses; core.Git implements it and has no
// force option and no refspec argument.
type Git interface {
	// TreeOf returns the tree hash of the worktree's content, tracked and untracked, ignored files
	// skipped.
	TreeOf(ctx context.Context, worktree string) (string, error)
	// Head returns the commit id and tree hash of the worktree's checked-out commit.
	Head(ctx context.Context, worktree string) (commit, tree string, err error)
	// Info describes one commit of the worktree: its id, tree, parents and subject.
	Info(ctx context.Context, worktree, rev string) (CommitInfo, error)
	// CurrentBranch returns the komodo/ branch the worktree has checked out, or "" for a detached HEAD.
	CurrentBranch(ctx context.Context, worktree string) (string, error)
	// Commit records the worktree's content as one commit on the checked-out branch and returns its
	// id.
	Commit(ctx context.Context, worktree, message string) (commit string, err error)
	// Push pushes one komodo/ branch to its own name on the remote, with a plain push.
	Push(ctx context.Context, worktree, remote, branch string) error
	// RemoteTip returns the commit id the remote holds for a branch; the bool is false when it has
	// none.
	RemoteTip(ctx context.Context, worktree, remote, branch string) (string, bool, error)
	// Merge merges ref into the branch checked out in dir with the given message; it never rebases,
	// and a conflict aborts the merge.
	Merge(ctx context.Context, dir, ref, message string, noFF bool) (head string, err error)
	// Squash lands ref on the branch checked out in dir as one new commit with the branch tip as its only parent and ref's
	// tree; the tip must be in the history of ref.
	Squash(ctx context.Context, dir, ref, message string) (head string, err error)
	// IsAncestor reports whether anc is in the history of desc, or equal to it.
	IsAncestor(ctx context.Context, dir, anc, desc string) (bool, error)
	// AddWorktree adds a worktree at path on a komodo/ branch cut from base, creating the branch
	// when it does not exist.
	AddWorktree(ctx context.Context, clone, path, branch, base string) error
	// RemoveWorktree removes a worktree and refuses when it holds files the tip lacks.
	RemoveWorktree(ctx context.Context, clone, path string) error
	// WorktreeOf returns the linked worktree that has a komodo/ branch checked out.
	WorktreeOf(ctx context.Context, clone, branch string) (string, bool, error)
	// FetchBranch fetches one branch of the remote into its remote-tracking reference.
	FetchBranch(ctx context.Context, dir, remote, branch string) error
	// Landed lists the group landings on head since base, newest first, read from the merge commits
	// on its first-parent line.
	Landed(ctx context.Context, dir, base, head string) ([]Landing, error)
	// DiffStat returns git's one-line diff stat of head against its common ancestor with base.
	DiffStat(ctx context.Context, dir, base, head string) (string, error)
	// BranchExists reports whether a komodo/ branch exists locally or among the remote-tracking
	// references.
	BranchExists(ctx context.Context, clone, branch string) (bool, error)
	// CreateBranch cuts a komodo/ branch from base without checking it out.
	CreateBranch(ctx context.Context, clone, branch, base string) error
	// DefaultBranch returns the branch the remote's HEAD names, such as main.
	DefaultBranch(ctx context.Context, dir, remote string) (string, error)
	// UpstreamRemote returns the remote the branch tracks; the bool is false when it tracks none.
	UpstreamRemote(ctx context.Context, dir, branch string) (string, bool, error)
}

// CommitInfo describes one commit.
type CommitInfo = core.CommitInfo

// Landing is one group landing on an epic branch, read back from its merge commit.
type Landing = core.Landing

var _ Git = (*core.Git)(nil)

// Errors the git wrapper returns, re-exported so callers match them without importing core.
var (
	ErrCriticalRef   = core.ErrCriticalRef
	ErrConflict      = core.ErrConflict
	ErrDiverged      = core.ErrDiverged
	ErrPushRejected  = core.ErrPushRejected
	ErrTrailer       = core.ErrTrailer
	ErrBranchMissing = core.ErrBranchMissing
	ErrNoGitDir      = core.ErrNoGitDir
	ErrForce         = core.ErrForce
)
