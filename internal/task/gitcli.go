package task

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// GitCLI is the Git the Task Manager calls, over the host git runner and its rules.
type GitCLI struct {
	Repo  string // the main repository folder; new worktrees are cut from it
	Start string // start point of a new detached group worktree: the tip of the epic's branch
	Base  string // the commit the group's diff is taken against; HEAD when empty

	mu    sync.Mutex
	ptrs  map[string]*Pointer
	hosts map[string]*HostGit
}

var (
	_ Git             = (*GitCLI)(nil)
	_ PointerRecorder = (*GitCLI)(nil)
)

// RecordPointer keeps the exact text of the worktree's .git file for the checks before every host git call.
func (g *GitCLI) RecordPointer(l LeaseRef) error {
	p, err := RecordPointer(l.Worktree)
	if err != nil {
		return err
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.ptrs == nil {
		g.ptrs = map[string]*Pointer{}
	}

	g.ptrs[l.Worktree] = p
	delete(g.hosts, l.Worktree)
	return nil
}

// TreeHash returns the hash of the tree the worktree holds now.
func (g *GitCLI) TreeHash(ctx context.Context, l LeaseRef) (string, error) {
	h, err := g.host(l)
	if err != nil {
		return "", err
	}
	return h.TreeHash(ctx)
}

// Diff returns the unified diff of the worktree against the base commit.
func (g *GitCLI) Diff(ctx context.Context, l LeaseRef, paths []string) (string, error) {
	h, err := g.host(l)
	if err != nil {
		return "", err
	}
	return h.Diff(ctx, paths)
}

// ReadOnly runs log, show, blame or status.
func (g *GitCLI) ReadOnly(ctx context.Context, l LeaseRef, command string, args []string) (string, error) {
	h, err := g.host(l)
	if err != nil {
		return "", err
	}
	return h.ReadOnly(ctx, command, args)
}

// ResetTracked restores changed tracked files and removes untracked files that are not ignored.
func (g *GitCLI) ResetTracked(ctx context.Context, l LeaseRef) error {
	h, err := g.host(l)
	if err != nil {
		return err
	}
	if _, err := h.Run(ctx, "reset", "--hard", "-q"); err != nil {
		return err
	}
	_, err = h.Run(ctx, "clean", "-fdq")
	return err
}

// AddWorktree creates the lease's worktree detached at the start point, with no branch, unless the worktree exists.
func (g *GitCLI) AddWorktree(ctx context.Context, l LeaseRef) error {
	if _, err := os.Stat(filepath.Join(l.Worktree, ".git")); err == nil {
		return nil
	}

	bin, err := lookGit()
	if err != nil {
		return err
	}

	empty, err := os.MkdirTemp("", "komodo-git-empty-")
	if err != nil {
		return fmt.Errorf("make the empty hooks folder: %w", err)
	}

	defer func() { _ = os.RemoveAll(empty) }()

	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + empty,
		"LANG=C",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
	}

	argv := []string{
		bin,
		"-C",
		g.Repo,
		"-c",
		"core.hooksPath=" + empty,
		"-c",
		"core.fsmonitor=",
		"worktree",
		"add",
		"-q",
		"--detach",
		l.Worktree,
	}
	if g.Start != "" {
		argv = append(argv, g.Start)
	}
	if _, err := hostOutput(ctx, g.Repo, env, "git add worktree", hostTimeout, argv...); err != nil {
		return fmt.Errorf("add the detached worktree %s: %w", l.Worktree, err)
	}

	return nil
}

// Close removes the runners' temporary folders.
func (g *GitCLI) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	var first error
	for _, h := range g.hosts {
		if err := h.Close(); err != nil && first == nil {
			first = err
		}
	}

	g.hosts = nil
	return first
}

// host returns the runner of a lease, recording the pointer first when nothing has yet.
func (g *GitCLI) host(l LeaseRef) (*HostGit, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if h := g.hosts[l.Worktree]; h != nil {
		return h, nil
	}

	p := g.ptrs[l.Worktree]
	if p == nil {
		var err error
		if p, err = RecordPointer(l.Worktree); err != nil {
			return nil, err
		}
		if g.ptrs == nil {
			g.ptrs = map[string]*Pointer{}
		}
		g.ptrs[l.Worktree] = p
	}

	h, err := NewHostGit(l, g.Base, p)
	if err != nil {
		return nil, err
	}
	if g.hosts == nil {
		g.hosts = map[string]*HostGit{}
	}

	g.hosts[l.Worktree] = h
	return h, nil
}

// changedPaths lists the files a unified diff changes, as the status lines of a tracked edit do not.
func trackedEdits(status string) []string {
	var out []string
	for _, l := range strings.Split(status, "\n") {
		if len(l) > 3 && !strings.HasPrefix(l, "??") && !strings.HasPrefix(l, "!!") {
			out = append(out, strings.TrimSpace(l[3:]))
		}
	}
	return out
}
