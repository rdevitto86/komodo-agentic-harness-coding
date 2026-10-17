package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// trailerLine matches one Name: value line.
	trailerLine = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*:[ \t]+\S`)
	// paragraphBreak splits a message at blank lines.
	paragraphBreak = regexp.MustCompile(`\n[ \t]*\n+`)
)

// TreeOf hashes the worktree's content, tracked and untracked with ignored files skipped, through a
// throwaway index.
func (g *Git) TreeOf(ctx context.Context, worktree string) (string, error) {
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
func (g *Git) Head(ctx context.Context, worktree string) (string, string, error) {
	i, err := g.Info(ctx, worktree, "HEAD")

	return i.ID, i.Tree, err
}

// Info describes one commit of the worktree.
func (g *Git) Info(ctx context.Context, worktree, rev string) (CommitInfo, error) {
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

	parents := strings.Fields(f[2])

	info := CommitInfo{ID: f[0], Tree: f[1], Subject: f[3]}
	if len(parents) > 0 {
		info.Parent = parents[0]
	}

	if len(parents) > 1 {
		info.Second = parents[1]
	}

	return info, nil
}

// Commit records the worktree's content as one commit on the checked-out komodo/ branch and returns
// its id. Content equal to the tip's returns the tip with no new commit.
func (g *Git) Commit(ctx context.Context, worktree, message string) (string, error) {
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

	if _, err := g.run(ctx, t, "", nil, "add", "-A"); err != nil {
		return "", err
	}

	out, err := g.run(ctx, t, "", nil, "write-tree")
	if err != nil {
		return "", err
	}

	if g.hasHead(ctx, t) {
		if id, tree, err := g.Head(ctx, worktree); err == nil && tree == strings.TrimSpace(out) {
			return id, nil
		}
	}

	if _, err := g.run(ctx, t, message, nil, "commit", "-q", "--no-verify", "-F", "-"); err != nil {
		return "", err
	}

	id, _, err := g.Head(ctx, worktree)

	return id, err
}

// Push sends one komodo/ branch to its own name on the remote with a plain refspec.
func (g *Git) Push(ctx context.Context, worktree, remote, branch string) error {
	if !ValidBranch(branch) {
		return fmt.Errorf("push %q: %w", branch, ErrCriticalRef)
	}

	if !safeName(remote) {
		return fmt.Errorf("remote name %q is not usable", remote)
	}

	t, err := g.locate(worktree)
	if err != nil {
		return err
	}

	_, err = g.runNet(
		ctx, t, "push", "--no-verify", remote, "refs/heads/"+branch+":refs/heads/"+branch,
	)
	if err == nil {
		return nil
	}

	if s := stderrOf(err); strings.Contains(s, "[rejected]") ||
		strings.Contains(s, "[remote rejected]") ||
		strings.Contains(s, "non-fast-forward") {
		return fmt.Errorf("%w: %s", ErrPushRejected, s)
	}

	return err
}

// RemoteTip reads the commit the remote holds for a branch with ls-remote.
func (g *Git) RemoteTip(
	ctx context.Context,
	worktree, remote, branch string,
) (string, bool, error) {
	if !ValidBranch(branch) {
		return "", false, fmt.Errorf("branch %q: %w", branch, ErrCriticalRef)
	}

	if !safeName(remote) {
		return "", false, fmt.Errorf("remote name %q is not usable", remote)
	}

	t, err := g.locate(worktree)
	if err != nil {
		return "", false, err
	}

	out, err := g.runNet(ctx, t, "ls-remote", "--heads", remote, "refs/heads/"+branch)
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
func (g *Git) RemoveWorktree(ctx context.Context, clone, path string) error {
	ct, err := g.ofClone(clone)
	if err != nil {
		return err
	}

	if _, err := os.Lstat(path); os.IsNotExist(err) {
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

// DiscardWorktree removes a worktree and its files even when they hold work the tip lacks; a link
// in its place is unlinked.
func (g *Git) DiscardWorktree(ctx context.Context, clone, path string) error {
	ct, err := g.ofClone(clone)
	if err != nil {
		return err
	}

	fi, err := os.Lstat(path)
	switch {
	case os.IsNotExist(err):
	case err != nil:
		return fmt.Errorf("inspect %s: %w", path, err)
	case fi.Mode()&os.ModeSymlink != 0:
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("unlink %s: %w", path, err)
		}
	default:
		t, lerr := g.locate(path)
		if lerr != nil {
			return lerr
		}

		if t.work == "" || realPath(path) == realPath(filepath.Dir(ct.gitDir)) {
			return fmt.Errorf("%s: %w", path, ErrCriticalRef)
		}

		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove %s: %w", path, err)
		}
	}

	_, err = g.run(ctx, ct, "", nil, "worktree", "prune")

	return err
}

// BranchTip returns the commit a local komodo/ branch points at; the bool is false when the branch
// does not exist.
func (g *Git) BranchTip(ctx context.Context, clone, branch string) (string, bool, error) {
	if !ValidBranch(branch) {
		return "", false, fmt.Errorf("branch %q: %w", branch, ErrCriticalRef)
	}

	t, err := g.ofClone(clone)
	if err != nil {
		return "", false, err
	}

	return g.branchTip(ctx, t, branch)
}

// DeleteBranch deletes a local komodo/ branch at expectTip whose commit is in the history of
// containedIn.
func (g *Git) DeleteBranch(
	ctx context.Context,
	clone, branch, expectTip, containedIn string,
) error {
	if !ValidBranch(branch) {
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

	if _, err := g.run(
		ctx, t, "", nil, "merge-base", "--is-ancestor", expectTip, containedIn,
	); err != nil {
		if exitCode(err) == 1 {
			return fmt.Errorf("%s in %s: %w", expectTip, containedIn, ErrNotContained)
		}

		return err
	}

	_, err = g.run(ctx, t, "", nil, "update-ref", "-d", "refs/heads/"+branch, expectTip)

	return err
}

// DiscardBranch deletes a local komodo/ branch that still points at expectTip.
func (g *Git) DiscardBranch(ctx context.Context, clone, branch, expectTip string) error {
	if !ValidBranch(branch) {
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

// Merge merges ref into the komodo/ branch checked out in dir; a conflict aborts the merge and is
// ErrConflict.
func (g *Git) Merge(ctx context.Context, dir, ref, message string, noFF bool) (string, error) {
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

	_, err = g.run(ctx, t, "", nil, "merge", "--no-edit", "--no-verify", ff, "-F", f.Name(), ref)
	if err != nil {
		if _, herr := g.run(
			ctx, t, "", nil, "rev-parse", "-q", "--verify", "MERGE_HEAD",
		); herr == nil {
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

// IsAncestor reports whether commit anc is in the history of desc, or equal to it.
func (g *Git) IsAncestor(ctx context.Context, dir, anc, desc string) (bool, error) {
	if !safeRev(anc) || !safeRev(desc) {
		return false, fmt.Errorf("revisions %q and %q are not usable", anc, desc)
	}

	t, err := g.locate(dir)
	if err != nil {
		return false, err
	}

	if _, err := g.run(ctx, t, "", nil, "merge-base", "--is-ancestor", anc, desc); err != nil {
		if exitCode(err) == 1 {
			return false, nil
		}

		return false, err
	}

	return true, nil
}

// AddWorktree adds a worktree on a komodo/ branch, cutting the branch from base when it does not
// exist; a repeat is a no-op.
func (g *Git) AddWorktree(ctx context.Context, clone, path, branch, base string) error {
	if !ValidBranch(branch) {
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

// WorktreeOf returns the path of a linked worktree that has the komodo/ branch checked out; the
// main checkout is never named.
func (g *Git) WorktreeOf(ctx context.Context, clone, branch string) (string, bool, error) {
	if !ValidBranch(branch) {
		return "", false, fmt.Errorf("branch %q: %w", branch, ErrCriticalRef)
	}

	ct, err := g.ofClone(clone)
	if err != nil {
		return "", false, err
	}

	out, err := g.run(ctx, ct, "", nil, "worktree", "list", "--porcelain")
	if err != nil {
		return "", false, err
	}

	for i, block := range strings.Split(strings.TrimSpace(out), "\n\n") {
		if i == 0 {
			continue
		}

		var path string

		for _, line := range strings.Split(block, "\n") {
			if p, ok := strings.CutPrefix(line, "worktree "); ok {
				path = p
			}

			if line == "branch refs/heads/"+branch && path != "" {
				return path, true, nil
			}
		}
	}

	return "", false, nil
}

// FetchBranch fetches one branch of the remote into its remote-tracking reference; a missing branch
// is ErrBranchMissing. The tracking reference follows the remote even when its history was
// rewritten.
func (g *Git) FetchBranch(ctx context.Context, dir, remote, branch string) error {
	if !safeName(branch) || strings.Contains(branch, "..") || !safeName(remote) {
		return fmt.Errorf("fetch %s/%s: name is not usable", remote, branch)
	}

	t, err := g.locate(dir)
	if err != nil {
		return err
	}

	_, err = g.runNet(
		ctx, t, "fetch", "--no-tags", remote,
		"+refs/heads/"+branch+":refs/remotes/"+remote+"/"+branch,
	)
	if err != nil && strings.Contains(stderrOf(err), "couldn't find remote ref") {
		return fmt.Errorf("%s/%s: %w", remote, branch, ErrBranchMissing)
	}

	return err
}

// Fetch brings every branch of the remote into its remote-tracking references; it never prunes.
func (g *Git) Fetch(ctx context.Context, clone, remote string) error {
	if !safeName(remote) {
		return fmt.Errorf("remote name %q is not usable", remote)
	}

	t, err := g.ofClone(clone)
	if err != nil {
		return err
	}

	_, err = g.runNet(
		ctx, t, "fetch", "--no-tags", "--no-prune", remote,
		"+refs/heads/*:refs/remotes/"+remote+"/*",
	)

	return err
}

// Holds reads the remote-tracking references and reports whether tip is in the history of contains,
// or of any branch of the remote when contains is empty; it returns the reference that holds it.
func (g *Git) Holds(
	ctx context.Context,
	clone, remote, contains, tip string,
) (string, bool, error) {
	if !safeName(remote) || !safeRev(tip) {
		return "", false, fmt.Errorf("remote %q or commit %q is not usable", remote, tip)
	}

	t, err := g.ofClone(clone)
	if err != nil {
		return "", false, err
	}

	refs := []string{contains}
	if contains == "" {
		out, err := g.run(
			ctx, t, "", nil, "for-each-ref", "--format=%(refname:short)",
			"refs/remotes/"+remote+"/",
		)
		if err != nil {
			return "", false, err
		}

		refs = strings.Fields(out)
	}

	for _, name := range refs {
		if name == "" || strings.HasSuffix(name, "/HEAD") || !safeName(name) {
			continue
		}

		full := strings.TrimPrefix(name, "refs/remotes/")

		full = "refs/remotes/" + full
		if _, err := g.run(
			ctx, t, "", nil, "rev-parse", "-q", "--verify", full+"^{commit}",
		); err != nil {
			continue
		}

		if _, err := g.run(ctx, t, "", nil, "merge-base", "--is-ancestor", tip, full); err == nil {
			return name, true, nil
		}
	}

	return "", false, nil
}

// Landed lists the group landings on head since base, newest first: merge commits on head's
// first-parent line with two parents and a body that starts with the group section.
func (g *Git) Landed(ctx context.Context, dir, base, head string) ([]Landing, error) {
	if !safeRev(base) || !safeRev(head) {
		return nil, fmt.Errorf("range %q..%q is not usable", base, head)
	}

	t, err := g.locate(dir)
	if err != nil {
		return nil, err
	}

	out, err := g.run(
		ctx, t, "", nil, "log", "--first-parent", "--merges", "--format=%P%x1f%s%x1f%b%x1e",
		base+".."+head, "--",
	)
	if err != nil {
		return nil, err
	}

	var list []Landing

	for _, rec := range strings.Split(out, "\x1e") {
		f := strings.SplitN(strings.TrimLeft(rec, "\n"), "\x1f", 3)
		if len(f) != 3 || len(strings.Fields(f[0])) != 2 {
			continue
		}

		label, goal, ok := strings.Cut(f[1], ": ")

		section := strings.TrimSpace(f[2])
		if !ok || !strings.HasPrefix(section, SectionMarker) {
			continue
		}

		list = append(list, Landing{Label: label, Goal: goal, Section: section})
	}

	return list, nil
}

// DiffStat returns git's one-line diff stat of head against its common ancestor with base.
func (g *Git) DiffStat(ctx context.Context, dir, base, head string) (string, error) {
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

// BranchExists reports whether a komodo/ branch exists locally or among the remote-tracking
// references.
func (g *Git) BranchExists(ctx context.Context, clone, branch string) (bool, error) {
	if !ValidBranch(branch) {
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
func (g *Git) CreateBranch(ctx context.Context, clone, branch, base string) error {
	if !ValidBranch(branch) {
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

// DefaultBranch returns the branch the remote's HEAD names, from the fetched reference or else from
// the remote itself.
func (g *Git) DefaultBranch(ctx context.Context, dir, remote string) (string, error) {
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

	out, err := g.runNet(ctx, t, "ls-remote", "--symref", remote, "HEAD")
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
func (g *Git) UpstreamRemote(ctx context.Context, dir, branch string) (string, bool, error) {
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

// hasHead reports whether the target's HEAD names a commit.
func (g *Git) hasHead(ctx context.Context, t target) bool {
	_, err := g.run(ctx, t, "", nil, "rev-parse", "-q", "--verify", "HEAD")

	return err == nil
}

// branchTip returns the commit a local branch points at; the bool is false when the branch is
// absent.
func (g *Git) branchTip(ctx context.Context, t target, branch string) (string, bool, error) {
	out, err := g.run(
		ctx, t, "", nil, "rev-parse", "-q", "--verify", "refs/heads/"+branch+"^{commit}",
	)
	if err != nil {
		if exitCode(err) == 1 {
			return "", false, nil
		}

		return "", false, err
	}

	return strings.TrimSpace(out), true, nil
}

// CheckMessage refuses an empty message and one whose last paragraph is only Name: value lines.
func CheckMessage(msg string) error {
	m := strings.TrimSpace(strings.ReplaceAll(msg, "\r\n", "\n"))
	if m == "" {
		return ErrEmptyMessage
	}

	paras := paragraphBreak.Split(m, -1)
	if len(paras) < 2 {
		return nil
	}

	for _, line := range strings.Split(paras[len(paras)-1], "\n") {
		if !trailerLine.MatchString(line) {
			return nil
		}
	}

	return ErrTrailer
}
