package commit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// hasHead reports whether the target's HEAD names a commit.
func (g *ExecGit) hasHead(ctx context.Context, t target) bool {
	_, err := g.run(ctx, t, "", nil, "rev-parse", "-q", "--verify", "HEAD")
	return err == nil
}

// TreeOf hashes the worktree's content, tracked and untracked with ignored files skipped, through a throwaway index.
func (g *ExecGit) TreeOf(ctx context.Context, worktree string) (string, error) {
	t, err := g.locate(worktree)
	if err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp("", "komodo-index-")
	if err != nil {
		return "", fmt.Errorf("create temporary index folder: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(tmp, "index")}
	if g.hasHead(ctx, t) {
		if _, err := g.run(ctx, t, "", env, "read-tree", "HEAD"); err != nil {
			return "", err
		}
	}
	if _, err := g.run(ctx, t, "", env, "add", "-A"); err != nil {
		return "", err
	}
	out, err := g.run(ctx, t, "", env, "write-tree")
	return strings.TrimSpace(out), err
}

// Head returns the checked-out commit and its tree.
func (g *ExecGit) Head(ctx context.Context, worktree string) (string, string, error) {
	i, err := g.Info(ctx, worktree, "HEAD")
	return i.ID, i.Tree, err
}

// Info describes one commit of the worktree.
func (g *ExecGit) Info(ctx context.Context, worktree, rev string) (CommitInfo, error) {
	if !safeRev(rev) {
		return CommitInfo{}, fmt.Errorf("revision %q: %w", rev, ErrCriticalRef)
	}
	t, err := g.locate(worktree)
	if err != nil {
		return CommitInfo{}, err
	}
	out, err := g.run(ctx, t, "", nil, "log", "-1", "--format=%H%x00%T%x00%P%x00%s", rev, "--")
	if err != nil {
		return CommitInfo{}, err
	}
	f := strings.SplitN(strings.TrimRight(out, "\n"), "\x00", 4)
	if len(f) != 4 {
		return CommitInfo{}, fmt.Errorf("read commit %s: unexpected output %q", rev, out)
	}
	parent, _, _ := strings.Cut(f[2], " ")
	return CommitInfo{ID: f[0], Tree: f[1], Parent: parent, Subject: f[3]}, nil
}

// Commit records the worktree's content as one commit on the checked-out komodo/ branch; unchanged content returns the tip.
func (g *ExecGit) Commit(ctx context.Context, worktree, message string) (string, error) {
	if err := CheckMessage(message); err != nil {
		return "", err
	}
	t, err := g.locate(worktree)
	if err != nil {
		return "", err
	}
	if _, err := g.currentBranch(ctx, t); err != nil {
		return "", err
	}
	want, err := g.TreeOf(ctx, worktree)
	if err != nil {
		return "", err
	}
	if g.hasHead(ctx, t) {
		if id, tree, err := g.Head(ctx, worktree); err == nil && tree == want {
			return id, nil
		}
	}
	if _, err := g.run(ctx, t, "", nil, "add", "-A"); err != nil {
		return "", err
	}
	if _, err := g.run(ctx, t, message, nil, "commit", "-q", "-F", "-"); err != nil {
		return "", err
	}
	id, _, err := g.Head(ctx, worktree)
	return id, err
}

// Push sends one komodo/ branch to its own name on the remote with a plain refspec.
func (g *ExecGit) Push(ctx context.Context, worktree, remote, branch string) error {
	if !validBranch(branch) {
		return fmt.Errorf("push %q: %w", branch, ErrCriticalRef)
	}
	if !safeName(remote) {
		return fmt.Errorf("remote name %q is not usable", remote)
	}
	t, err := g.locate(worktree)
	if err != nil {
		return err
	}
	_, err = g.run(ctx, t, "", nil, "push", remote, "refs/heads/"+branch+":refs/heads/"+branch)
	if err == nil {
		return nil
	}
	if s := stderrOf(err); strings.Contains(s, "[rejected]") || strings.Contains(s, "[remote rejected]") || strings.Contains(s, "non-fast-forward") {
		return fmt.Errorf("%w: %s", ErrPushRejected, s)
	}
	return err
}

// RemoteTip reads the commit the remote holds for a branch with ls-remote.
func (g *ExecGit) RemoteTip(ctx context.Context, worktree, remote, branch string) (string, bool, error) {
	if !validBranch(branch) {
		return "", false, fmt.Errorf("branch %q: %w", branch, ErrCriticalRef)
	}
	if !safeName(remote) {
		return "", false, fmt.Errorf("remote name %q is not usable", remote)
	}
	t, err := g.locate(worktree)
	if err != nil {
		return "", false, err
	}
	out, err := g.run(ctx, t, "", nil, "ls-remote", "--heads", remote, "refs/heads/"+branch)
	if err != nil {
		return "", false, err
	}
	for _, line := range strings.Split(out, "\n") {
		sha, ref, ok := strings.Cut(line, "\t")
		if ok && ref == "refs/heads/"+branch {
			return sha, true, nil
		}
	}
	return "", false, nil
}

// RemoveWorktree removes a clean worktree; one holding files the tip lacks is ErrDirty.
func (g *ExecGit) RemoveWorktree(ctx context.Context, clone, path string) error {
	ct, err := g.ofClone(clone)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		_, err = g.run(ctx, ct, "", nil, "worktree", "prune")
		return err
	}
	_, headTree, err := g.Head(ctx, path)
	if err != nil {
		return err
	}
	tree, err := g.TreeOf(ctx, path)
	if err != nil {
		return err
	}
	if tree != headTree {
		return fmt.Errorf("%s: %w", path, ErrDirty)
	}
	if _, err := g.run(ctx, ct, "", nil, "worktree", "remove", "--", path); err != nil {
		return err
	}
	_, err = g.run(ctx, ct, "", nil, "worktree", "prune")
	return err
}

// branchTip returns the commit a local branch points at; the bool is false when the branch is absent.
func (g *ExecGit) branchTip(ctx context.Context, t target, branch string) (string, bool, error) {
	out, err := g.run(ctx, t, "", nil, "rev-parse", "-q", "--verify", "refs/heads/"+branch+"^{commit}")
	if err != nil {
		if exitCode(err) == 1 {
			return "", false, nil
		}
		return "", false, err
	}
	return strings.TrimSpace(out), true, nil
}

// DeleteBranch deletes a local komodo/ branch at expectTip whose commit is in the history of containedIn.
func (g *ExecGit) DeleteBranch(ctx context.Context, clone, branch, expectTip, containedIn string) error {
	if !validBranch(branch) {
		return fmt.Errorf("delete %q: %w", branch, ErrCriticalRef)
	}
	if !safeRev(expectTip) || !safeRev(containedIn) {
		return fmt.Errorf("revision for %q is not usable", branch)
	}
	t, err := g.ofClone(clone)
	if err != nil {
		return err
	}
	tip, ok, err := g.branchTip(ctx, t, branch)
	if err != nil || !ok {
		return err
	}
	if tip != expectTip {
		return fmt.Errorf("%s is at %s, expected %s: %w", branch, tip, expectTip, ErrTipMoved)
	}
	if _, err := g.run(ctx, t, "", nil, "merge-base", "--is-ancestor", expectTip, containedIn); err != nil {
		if exitCode(err) == 1 {
			return fmt.Errorf("%s in %s: %w", expectTip, containedIn, ErrNotContained)
		}
		return err
	}
	_, err = g.run(ctx, t, "", nil, "update-ref", "-d", "refs/heads/"+branch, expectTip)
	return err
}

// DiscardBranch deletes a local komodo/ branch that still points at expectTip.
func (g *ExecGit) DiscardBranch(ctx context.Context, clone, branch, expectTip string) error {
	if !validBranch(branch) {
		return fmt.Errorf("discard %q: %w", branch, ErrCriticalRef)
	}
	if !safeRev(expectTip) {
		return fmt.Errorf("commit %q is not usable", expectTip)
	}
	t, err := g.ofClone(clone)
	if err != nil {
		return err
	}
	tip, ok, err := g.branchTip(ctx, t, branch)
	if err != nil || !ok {
		return err
	}
	if tip != expectTip {
		return fmt.Errorf("%s is at %s, expected %s: %w", branch, tip, expectTip, ErrTipMoved)
	}
	_, err = g.run(ctx, t, "", nil, "update-ref", "-d", "refs/heads/"+branch, expectTip)
	return err
}

// Merge merges ref into the komodo/ branch checked out in dir; a conflict aborts the merge and is ErrConflict.
func (g *ExecGit) Merge(ctx context.Context, dir, ref, message string, noFF bool) (string, error) {
	if !safeRev(ref) {
		return "", fmt.Errorf("merge ref %q is not usable", ref)
	}
	if err := CheckMessage(message); err != nil {
		return "", err
	}
	t, err := g.locate(dir)
	if err != nil {
		return "", err
	}
	if _, err := g.currentBranch(ctx, t); err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "komodo-merge-msg-")
	if err != nil {
		return "", fmt.Errorf("write merge message: %w", err)
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, werr := f.WriteString(message)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return "", fmt.Errorf("write merge message: %w", werr)
	}
	ff := "--ff"
	if noFF {
		ff = "--no-ff"
	}
	_, err = g.run(ctx, t, "", nil, "merge", "--no-edit", ff, "-F", f.Name(), ref)
	if err != nil {
		if _, herr := g.run(ctx, t, "", nil, "rev-parse", "-q", "--verify", "MERGE_HEAD"); herr == nil {
			if _, aerr := g.run(ctx, t, "", nil, "merge", "--abort"); aerr != nil {
				return "", errors.Join(ErrConflict, aerr)
			}
			return "", fmt.Errorf("%s into %s: %w", ref, dir, ErrConflict)
		}
		return "", err
	}
	id, _, err := g.Head(ctx, dir)
	return id, err
}

// AddWorktree adds a worktree on a komodo/ branch, cutting the branch from base when it does not exist; a repeat is a no-op.
func (g *ExecGit) AddWorktree(ctx context.Context, clone, path, branch, base string) error {
	if !validBranch(branch) {
		return fmt.Errorf("worktree branch %q: %w", branch, ErrCriticalRef)
	}
	ct, err := g.ofClone(clone)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", path, err)
	}
	if t, lerr := g.locate(abs); lerr == nil {
		if cur, err := g.currentBranch(ctx, t); err == nil && cur == branch {
			return nil
		}
		return fmt.Errorf("%s is a worktree of another branch", abs)
	}
	_, exists, err := g.branchTip(ctx, ct, branch)
	if err != nil {
		return err
	}
	if exists {
		_, err = g.run(ctx, ct, "", nil, "worktree", "add", "--", abs, branch)
		return err
	}
	if !safeRev(base) {
		return fmt.Errorf("base %q is not usable", base)
	}
	_, err = g.run(ctx, ct, "", nil, "worktree", "add", "--no-track", "-b", branch, "--", abs, base)
	return err
}

// Fetch fetches one branch of the remote into its remote-tracking reference; a missing branch is ErrBranchMissing.
func (g *ExecGit) Fetch(ctx context.Context, dir, remote, branch string) error {
	if !safeName(branch) || strings.Contains(branch, "..") || !safeName(remote) {
		return fmt.Errorf("fetch %s/%s: name is not usable", remote, branch)
	}
	t, err := g.locate(dir)
	if err != nil {
		return err
	}
	_, err = g.run(ctx, t, "", nil, "fetch", "--no-tags", remote, "refs/heads/"+branch+":refs/remotes/"+remote+"/"+branch)
	if err != nil && strings.Contains(stderrOf(err), "couldn't find remote ref") {
		return fmt.Errorf("%s/%s: %w", remote, branch, ErrBranchMissing)
	}
	return err
}

// Landed lists the group landings on head since base, newest first, from the merge commits on head's first-parent line.
func (g *ExecGit) Landed(ctx context.Context, dir, base, head string) ([]Landing, error) {
	if !safeRev(base) || !safeRev(head) {
		return nil, fmt.Errorf("range %q..%q is not usable", base, head)
	}
	t, err := g.locate(dir)
	if err != nil {
		return nil, err
	}
	out, err := g.run(ctx, t, "", nil, "log", "--first-parent", "--merges", "--format=%H%x1f%s%x1f%b%x1e", base+".."+head, "--")
	if err != nil {
		return nil, err
	}
	var list []Landing
	for _, rec := range strings.Split(out, "\x1e") {
		f := strings.SplitN(strings.TrimLeft(rec, "\n"), "\x1f", 3)
		if len(f) != 3 {
			continue
		}
		label, goal, ok := strings.Cut(f[1], ": ")
		if !ok {
			continue
		}
		files, err := g.run(ctx, t, "", nil, "diff", "--name-only", f[0]+"^1", f[0], "--")
		if err != nil {
			return nil, err
		}
		l := Landing{Label: label, Goal: goal, Section: strings.TrimSpace(f[2]), Files: []string{}}
		for _, name := range strings.Split(files, "\n") {
			if name != "" {
				l.Files = append(l.Files, name)
			}
		}
		list = append(list, l)
	}
	return list, nil
}

// DiffStat returns git's one-line diff stat of head against its common ancestor with base.
func (g *ExecGit) DiffStat(ctx context.Context, dir, base, head string) (string, error) {
	if !safeRev(base) || !safeRev(head) {
		return "", fmt.Errorf("range %q...%q is not usable", base, head)
	}
	t, err := g.locate(dir)
	if err != nil {
		return "", err
	}
	out, err := g.run(ctx, t, "", nil, "diff", "--shortstat", base+"..."+head, "--")
	return strings.TrimSpace(out), err
}

// BranchExists reports whether a komodo/ branch exists locally or among the remote-tracking references.
func (g *ExecGit) BranchExists(ctx context.Context, clone, branch string) (bool, error) {
	if !validBranch(branch) {
		return false, fmt.Errorf("branch %q: %w", branch, ErrCriticalRef)
	}
	t, err := g.ofClone(clone)
	if err != nil {
		return false, err
	}
	if _, ok, err := g.branchTip(ctx, t, branch); err != nil || ok {
		return ok, err
	}
	out, err := g.run(ctx, t, "", nil, "for-each-ref", "--format=%(refname)", "refs/remotes")
	if err != nil {
		return false, err
	}
	for _, ref := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(ref, "refs/remotes/")
		if _, name, found := strings.Cut(rest, "/"); ok && found && name == branch {
			return true, nil
		}
	}
	return false, nil
}

// CreateBranch cuts a komodo/ branch from base without checking it out.
func (g *ExecGit) CreateBranch(ctx context.Context, clone, branch, base string) error {
	if !validBranch(branch) {
		return fmt.Errorf("create %q: %w", branch, ErrCriticalRef)
	}
	if !safeRev(base) {
		return fmt.Errorf("base %q is not usable", base)
	}
	t, err := g.ofClone(clone)
	if err != nil {
		return err
	}
	_, err = g.run(ctx, t, "", nil, "branch", "--no-track", "--", branch, base)
	return err
}

// DefaultBranch returns the branch the remote's HEAD names, from the fetched reference or else from the remote itself.
func (g *ExecGit) DefaultBranch(ctx context.Context, dir, remote string) (string, error) {
	if !safeName(remote) {
		return "", fmt.Errorf("remote name %q is not usable", remote)
	}
	t, err := g.locate(dir)
	if err != nil {
		return "", err
	}
	prefix := "refs/remotes/" + remote + "/"
	if out, err := g.run(ctx, t, "", nil, "symbolic-ref", "-q", prefix+"HEAD"); err == nil {
		if name, ok := strings.CutPrefix(strings.TrimSpace(out), prefix); ok {
			return name, nil
		}
	}
	out, err := g.run(ctx, t, "", nil, "ls-remote", "--symref", remote, "HEAD")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if ref, ok := strings.CutPrefix(line, "ref: refs/heads/"); ok {
			name, _, _ := strings.Cut(ref, "\t")
			return name, nil
		}
	}
	return "", fmt.Errorf("remote %s names no default branch", remote)
}

// UpstreamRemote returns the remote a local branch tracks; the bool is false when it tracks none.
func (g *ExecGit) UpstreamRemote(ctx context.Context, dir, branch string) (string, bool, error) {
	if !safeName(branch) {
		return "", false, fmt.Errorf("branch %q is not usable", branch)
	}
	t, err := g.locate(dir)
	if err != nil {
		return "", false, err
	}
	out, err := g.run(ctx, t, "", nil, "config", "--get", "branch."+branch+".remote")
	if err != nil {
		if exitCode(err) == 1 {
			return "", false, nil
		}
		return "", false, err
	}
	r := strings.TrimSpace(out)
	if r == "" || r == "." {
		return "", false, nil
	}
	return r, true, nil
}
