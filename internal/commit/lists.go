package commit

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// SaveLists commits the epic's task lists, from docs/backlog/, onto the local epic branch as a commit and
// pushes nothing; PushEpic sends it with the first landed group. It holds the epic's landing turn, and works
// in the landing worktree when the epic branch is checked out there.
func (h *Handler) SaveLists(ctx context.Context, r ListsRequest) error {
	epic := "komodo/" + r.Epic
	if !validBranch(epic) || strings.ContainsAny(r.Epic, "/\\") {
		return fail(KindCriticalRef, false, fmt.Errorf("epic branch %q: %w", epic, ErrCriticalRef))
	}

	h.adopt(r.CommonDir)
	h.adopt(r.Root)

	cfg := h.settings()
	base, remote := cfg.BaseBranch, cfg.Remote

	if err := resolveRemote(ctx, h.Git, r.Root, &base, &remote); err != nil {
		return err
	}

	if base == epic {
		return fail(
			KindCriticalRef,
			false,
			fmt.Errorf("base branch %q is the epic branch: %w", base, ErrCriticalRef),
		)
	}

	src, name, err := listsFolder(r.Root, r.Epic)
	if err != nil {
		return fail(KindGit, false, err)
	}

	turn, err := h.Epics.Lock(ctx, r.Repo+"/"+r.Epic)
	if err != nil {
		return fail(KindGit, true, fmt.Errorf("wait for the epic's turn: %w", err))
	}
	defer turn.Release()

	wt, release, err := h.epicWorktree(ctx, r.CommonDir, epic, remote)
	if err != nil {
		return err
	}
	defer release()

	if err := h.Git.FetchBranch(ctx, wt, remote, epic); err == nil {
		if _, err := h.Git.Merge(
			ctx, wt, "refs/remotes/"+remote+"/"+epic, r.Epic+": update from "+remote, false,
		); err != nil {
			return gitFail(err)
		}
	} else if !errors.Is(err, ErrBranchMissing) {
		return fail(KindPush, true, err)
	}

	dest := filepath.Join(wt, "docs", "backlog", name)
	if err := os.RemoveAll(dest); err != nil {
		return fail(KindGit, true, fmt.Errorf("clear %s: %w", dest, err))
	}

	if err := copyLists(src, dest); err != nil {
		return fail(KindGit, true, err)
	}

	if _, err := h.Git.Commit(ctx, wt, r.Epic+": task lists"); err != nil {
		return gitFail(err)
	}

	return nil
}

// epicWorktree returns a worktree with the epic branch checked out and a function that releases
// it: the landing worktree when it exists, which stays, otherwise a temporary one that is removed.
func (h *Handler) epicWorktree(ctx context.Context, common, epic, remote string) (string, func(), error) {
	if path, ok, err := h.Git.WorktreeOf(ctx, common, epic); err != nil {
		return "", nil, gitFail(err)
	} else if ok {
		return path, func() {}, nil
	}

	tmp, err := os.MkdirTemp("", "komodo-lists-")
	if err != nil {
		return "", nil, fail(
			KindGit,
			true,
			fmt.Errorf("create a temporary worktree folder: %w", err),
		)
	}

	wt := filepath.Join(tmp, "wt")
	if err := h.Git.AddWorktree(ctx, common, wt, epic, remote+"/"+epic); err != nil {
		_ = os.RemoveAll(tmp)
		return "", nil, gitFail(err)
	}

	return wt, func() {
		if h.Git.RemoveWorktree(ctx, common, wt) != nil {
			_ = os.RemoveAll(wt)
			_ = h.Git.RemoveWorktree(ctx, common, wt)
		}

		_ = os.RemoveAll(tmp)
	}, nil
}

// listsFolder finds the epic's folder under docs/backlog/: the label itself, or epic-N for a label
// ep-N.
func listsFolder(root, epic string) (path, name string, err error) {
	candidates := []string{epic}
	if n, ok := strings.CutPrefix(epic, "ep-"); ok {
		candidates = append(candidates, "epic-"+n)
	}

	for _, c := range candidates {
		p := filepath.Join(root, "docs", "backlog", c)
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			return p, c, nil
		}
	}

	return "", "", fmt.Errorf(
		"no task list folder for %s under %s",
		epic,
		filepath.Join(root, "docs", "backlog"),
	)
}

// copyLists copies the regular files under src to dest, skipping failed-plan folders and links.
func copyLists(src, dest string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}

		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".plan-") {
				return fs.SkipDir
			}
			return os.MkdirAll(filepath.Join(dest, rel), 0o755)
		}

		if !d.Type().IsRegular() {
			return nil
		}

		b, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read task list %s: %w", p, err)
		}

		mode := fs.FileMode(0o644)
		if fi, err := d.Info(); err == nil && fi.Mode()&0o111 != 0 {
			mode = 0o755
		}

		return os.WriteFile(filepath.Join(dest, rel), b, mode)
	})
}
