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

// SaveLists commits the epic's task lists, from docs/backlog/, onto the epic branch as a commit and pushes it.
func (h *Handler) SaveLists(ctx context.Context, r ListsRequest) error {
	epic := "komodo/" + r.Epic
	if !validBranch(epic) || strings.ContainsAny(r.Epic, "/\\") {
		return fail(KindCriticalRef, false, fmt.Errorf("epic branch %q: %w", epic, ErrCriticalRef))
	}
	src, name, err := listsFolder(r.Root, r.Epic)
	if err != nil {
		return fail(KindGit, false, err)
	}
	h.adopt(r.CommonDir)
	h.adopt(r.Root)
	var base, remote = h.Config.BaseBranch, h.Config.Remote
	if err := resolveRemote(ctx, h.Git, r.Root, &base, &remote); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "komodo-lists-")
	if err != nil {
		return fail(KindGit, true, fmt.Errorf("create a temporary worktree folder: %w", err))
	}
	defer os.RemoveAll(tmp)
	wt := filepath.Join(tmp, "wt")
	if err := h.Git.AddWorktree(ctx, r.CommonDir, wt, epic, remote+"/"+epic); err != nil {
		return gitFail(err)
	}
	defer func() {
		if h.Git.RemoveWorktree(ctx, r.CommonDir, wt) != nil {
			_ = os.RemoveAll(wt)
			_ = h.Git.RemoveWorktree(ctx, r.CommonDir, wt)
		}
	}()
	if err := h.Git.Fetch(ctx, wt, remote, epic); err == nil {
		if _, err := h.Git.Merge(ctx, wt, remote+"/"+epic, r.Epic+": update from "+remote, false); err != nil {
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
	local, _, err := h.Git.Head(ctx, wt)
	if err != nil {
		return gitFail(err)
	}
	if tip, ok, err := h.Git.RemoteTip(ctx, wt, remote, epic); err != nil {
		return fail(KindPush, true, err)
	} else if ok && tip == local {
		return nil
	}
	if err := h.Git.Push(ctx, wt, remote, epic); err != nil {
		if errors.Is(err, ErrPushRejected) {
			return gitFail(err)
		}
		return fail(KindPush, true, err)
	}
	return nil
}

// listsFolder finds the epic's folder under docs/backlog/: the label itself, or epic-N for a label ep-N.
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
	return "", "", fmt.Errorf("no task list folder for %s under %s", epic, filepath.Join(root, "docs", "backlog"))
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
