package cleanup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/fault"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/lease"
)

const (
	testRepo   = "github.com/acme/shop"
	testBranch = "komodo/ep-1-tg-1.2"
	testEpic   = "komodo/ep-1"
	epicRef    = "origin/komodo/ep-1"
)

// callLog records the calls the fakes receive, in order.
type callLog struct {
	mu    sync.Mutex
	calls []string
}

// add appends one call.
func (l *callLog) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, s)
}

// all returns a copy of the calls.
func (l *callLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

// names returns the calls that start with one of the prefixes, in order.
func (l *callLog) names(prefixes ...string) []string {
	var out []string
	for _, c := range l.all() {
		for _, p := range prefixes {
			if strings.HasPrefix(c, p) {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// fakeBoxes is a Containers that logs and can fail.
type fakeBoxes struct {
	log    *callLog
	err    error
	groups []string // groups that have a container
}

// Remove logs the removal.
func (b *fakeBoxes) Remove(_ context.Context, group string) error {
	b.log.add("container.Remove " + group)
	return b.err
}

// List returns the groups the fake docker holds containers for.
func (b *fakeBoxes) List(context.Context) ([]string, error) { return b.groups, nil }

// fakeTask is a TaskGroup that logs and can fail.
type fakeTask struct {
	log *callLog
	err error
}

// CloseTaskGroup logs the close.
func (t *fakeTask) CloseTaskGroup(context.Context) error {
	t.log.add("task.Close")
	return t.err
}

// fakeLeases serves lease records by group; after flipAfter reads a group's lease turns into a live run's.
type fakeLeases struct {
	mu        sync.Mutex
	recs      map[string]lease.Lease
	log       *callLog
	flipAfter int // reads before the holder changes; 0 means never
	reads     int
}

// Holder returns the record of a group, turning it into a new run's lease after flipAfter reads.
func (f *fakeLeases) Holder(_ context.Context, repo, group string) (lease.Lease, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if repo != testRepo {
		return lease.Lease{}, false, nil
	}
	l, ok := f.recs[group]
	f.reads++
	if ok && f.flipAfter > 0 && f.reads > f.flipAfter {
		l.Run = "new"
	}
	return l, ok, nil
}

// LockGroup logs the lock and its release.
func (f *fakeLeases) LockGroup(_ context.Context, _, group string) (func(), error) {
	f.log.add("lease.Lock " + group)
	return func() { f.log.add("lease.Unlock " + group) }, nil
}

// fakeGroups serves the unfinished groups.
type fakeGroups struct{ refs []GroupRef }

// Unfinished returns the configured groups.
func (f *fakeGroups) Unfinished(context.Context) ([]GroupRef, error) { return f.refs, nil }

// fact is one progress fact the reporter received.
type fact struct {
	group, subtype string
	data           any
}

// fakeReporter records progress facts.
type fakeReporter struct {
	mu    sync.Mutex
	facts []fact
}

// Report stores the fact.
func (r *fakeReporter) Report(_ context.Context, group, subtype string, data any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.facts = append(r.facts, fact{group, subtype, data})
	return nil
}

// subtypes returns the subtypes in arrival order.
func (r *fakeReporter) subtypes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, f := range r.facts {
		out = append(out, f.subtype)
	}
	return out
}

// fakeErrors records the errors sent to the Error Handler.
type fakeErrors struct {
	mu   sync.Mutex
	errs []error
}

// Report stores the error.
func (e *fakeErrors) Report(_ fault.Source, _ string, _ fault.Kind, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.errs = append(e.errs, err)
}

// count returns how many errors arrived.
func (e *fakeErrors) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.errs)
}

// refusal is an error a git wrapper returns to refuse on purpose.
type refusal struct{ msg string }

// Error returns the message.
func (r refusal) Error() string { return r.msg }

// Refused marks the error as a refusal.
func (r refusal) Refused() bool { return true }

// execGit is a Git over the real git program, for temporary repositories only.
type execGit struct{ t *testing.T }

// git runs git in dir with a clean configuration and returns its trimmed output.
func (g execGit) git(dir string, args ...string) (string, error) {
	return runGit(dir, args...)
}

// RemoveWorktree removes a clean worktree and refuses one that holds files.
func (g execGit) RemoveWorktree(_ context.Context, clone, path string) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		_, err := g.git(clone, "worktree", "prune")
		return err
	}
	out, err := g.git(path, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return err
	}
	if out != "" {
		return refusal{"worktree holds files the tip lacks"}
	}
	_, err = g.git(clone, "worktree", "remove", path)
	return err
}

// DiscardWorktree removes a worktree whatever it holds.
func (g execGit) DiscardWorktree(_ context.Context, clone, path string) error {
	if _, err := os.Stat(path); err == nil {
		if _, err := g.git(clone, "worktree", "remove", "--force", path); err != nil {
			return err
		}
	}
	_, err := g.git(clone, "worktree", "prune")
	return err
}

// BranchTip returns the commit a local branch points at.
func (g execGit) BranchTip(_ context.Context, clone, branch string) (string, bool, error) {
	out, err := g.git(clone, "rev-parse", "--verify", "-q", "refs/heads/"+branch)
	if err != nil {
		return "", false, nil
	}
	return out, true, nil
}

// DeleteBranch deletes a branch that still points at expectTip and is contained in containedIn.
func (g execGit) DeleteBranch(ctx context.Context, clone, branch, expectTip, containedIn string) error {
	tip, ok, _ := g.BranchTip(ctx, clone, branch)
	if !ok {
		return nil
	}
	if tip != expectTip {
		return refusal{"branch moved since publish"}
	}
	if _, err := g.git(clone, "merge-base", "--is-ancestor", tip, containedIn); err != nil {
		return refusal{"remote history lacks the tip"}
	}
	_, err := g.git(clone, "update-ref", "-d", "refs/heads/"+branch, expectTip)
	return err
}

// DiscardBranch deletes a branch that still points at expectTip.
func (g execGit) DiscardBranch(ctx context.Context, clone, branch, expectTip string) error {
	tip, ok, _ := g.BranchTip(ctx, clone, branch)
	if !ok {
		return nil
	}
	if tip != expectTip {
		return refusal{"branch moved"}
	}
	_, err := g.git(clone, "update-ref", "-d", "refs/heads/"+branch, expectTip)
	return err
}

// Fetch brings the remote's branches in without pruning.
func (g execGit) Fetch(_ context.Context, clone, remote string) error {
	_, err := g.git(clone, "fetch", remote)
	return err
}

// Holds fetches the remote and checks the tip against one reference or every remote branch.
func (g execGit) Holds(_ context.Context, clone, remote, contains, tip string) (string, bool, error) {
	refs := []string{contains}
	if contains == "" {
		out, err := g.git(clone, "for-each-ref", "--format=%(refname)", "refs/remotes/"+remote+"/")
		if err != nil {
			return "", false, err
		}
		refs = strings.Fields(out)
	}
	for _, r := range refs {
		if strings.HasSuffix(r, "/HEAD") {
			continue
		}
		if _, err := g.git(clone, "merge-base", "--is-ancestor", tip, r); err == nil {
			return r, true, nil
		}
	}
	return "", false, nil
}

// recGit logs the mutating calls of a Git and can fail chosen ones.
type recGit struct {
	Git
	log    *callLog
	failOn map[string]error // call name to the error it returns once
}

// hit logs a call and returns the one-shot failure planned for it.
func (r *recGit) hit(name, detail string) error {
	r.log.add(name + " " + detail)
	if err, ok := r.failOn[name]; ok {
		delete(r.failOn, name)
		return err
	}
	return nil
}

// Fetch logs and forwards.
func (r *recGit) Fetch(ctx context.Context, clone, remote string) error {
	if err := r.hit("git.Fetch", remote); err != nil {
		return err
	}
	return r.Git.Fetch(ctx, clone, remote)
}

// RemoveWorktree logs and forwards.
func (r *recGit) RemoveWorktree(ctx context.Context, clone, path string) error {
	if err := r.hit("git.RemoveWorktree", path); err != nil {
		return err
	}
	return r.Git.RemoveWorktree(ctx, clone, path)
}

// DiscardWorktree logs and forwards.
func (r *recGit) DiscardWorktree(ctx context.Context, clone, path string) error {
	if err := r.hit("git.DiscardWorktree", path); err != nil {
		return err
	}
	return r.Git.DiscardWorktree(ctx, clone, path)
}

// DeleteBranch logs and forwards.
func (r *recGit) DeleteBranch(ctx context.Context, clone, branch, tip, in string) error {
	if err := r.hit("git.DeleteBranch", branch); err != nil {
		return err
	}
	return r.Git.DeleteBranch(ctx, clone, branch, tip, in)
}

// DiscardBranch logs and forwards.
func (r *recGit) DiscardBranch(ctx context.Context, clone, branch, tip string) error {
	if err := r.hit("git.DiscardBranch", branch); err != nil {
		return err
	}
	return r.Git.DiscardBranch(ctx, clone, branch, tip)
}

// runGit runs git in dir with the user's configuration and hooks out of the way.
func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_TERMINAL_PROMPT=0",
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", errors.New("git " + strings.Join(args, " ") + ": " + err.Error() + ": " + errb.String())
	}
	return strings.TrimSpace(out.String()), nil
}

// grp is one group the fixture created.
type grp struct {
	id, branch, wt, tip string
}

// env is a temporary repository, a bare remote, a Komodo home and a Handler wired to fakes.
type env struct {
	t                   *testing.T
	base, remote, clone string
	roots               Roots
	log                 *callLog
	boxes               *fakeBoxes
	task                *fakeTask
	leases              *fakeLeases
	groups              *fakeGroups
	rep                 *fakeReporter
	errs                *fakeErrors
	git                 *recGit
	h                   *Handler
}

// newEnv builds the fixture: a bare remote, a clone with one commit on main, the roots and the handler.
func newEnv(t *testing.T) *env {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, base: base, remote: filepath.Join(base, "remote.git"), clone: filepath.Join(base, "clone")}
	e.roots = Roots{
		Worktrees: filepath.Join(base, "home", "wt", "shop"),
		Landings:  filepath.Join(base, "home", "landing", "shop"),
		Testers:   filepath.Join(base, "home", "testers"),
		Temp:      filepath.Join(base, "home", "tmp"),
		Versions:  filepath.Join(base, "home", "versions"),
	}
	for _, d := range []string{e.remote, e.clone, e.roots.Worktrees, e.roots.Landings, e.roots.Testers, e.roots.Temp, e.roots.Versions} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e.must(e.remote, "init", "--bare", "-b", "main")
	e.must(e.clone, "init", "-b", "main")
	e.must(e.clone, "remote", "add", "origin", e.remote)
	e.write(filepath.Join(e.clone, "README"), "base\n")
	e.must(e.clone, "add", "-A")
	e.must(e.clone, "commit", "-m", "base")
	e.must(e.clone, "push", "origin", "main")
	e.must(e.clone, "push", "origin", "main:refs/heads/"+testEpic)

	e.log = &callLog{}
	e.boxes = &fakeBoxes{log: e.log}
	e.task = &fakeTask{log: e.log}
	e.leases = &fakeLeases{recs: map[string]lease.Lease{}, log: e.log}
	e.groups = &fakeGroups{}
	e.rep = &fakeReporter{}
	e.errs = &fakeErrors{}
	e.git = &recGit{Git: execGit{t}, log: e.log, failOn: map[string]error{}}
	e.h = &Handler{
		Lease: e.leases, Git: e.git, Boxes: e.boxes, Task: e.task, Groups: e.groups, Report: e.rep, Errors: e.errs,
		Repo: testRepo, Clone: e.clone, Remote: "origin", Roots: e.roots,
	}
	return e
}

// must runs git in dir and fails the test on error.
func (e *env) must(dir string, args ...string) string {
	e.t.Helper()
	out, err := runGit(dir, args...)
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

// write writes a file, making its folder.
func (e *env) write(path, body string) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// addGroup creates a group's worktree and branch with one commit of work.
func (e *env) addGroup(id, branch string) grp {
	e.t.Helper()
	wt := filepath.Join(e.roots.Worktrees, id)
	e.must(e.clone, "worktree", "add", "-b", branch, wt, "main")
	e.write(filepath.Join(wt, "work-"+id+".txt"), "work\n")
	e.must(wt, "add", "-A")
	e.must(wt, "commit", "-m", "work "+id)
	return grp{id: id, branch: branch, wt: wt, tip: e.must(wt, "rev-parse", "HEAD")}
}

// land puts the group's tip on the remote's epic branch, as the Commit Handler's push does.
func (e *env) land(g grp) {
	e.t.Helper()
	e.must(e.clone, "push", "origin", g.tip+":refs/heads/"+testEpic)
}

// published returns the Published fact of a landed group.
func (e *env) published(g grp) Published {
	return Published{
		Run: "run-1", Group: g.id, Epic: "ep-1", Branch: g.branch, Tip: g.tip, Remote: "origin",
		Contains: epicRef, Worktree: g.wt,
	}
}

// holdLease records a lease for the group, live or dead as the fixture's Live check says.
func (e *env) holdLease(g grp, state lease.LeaseState) {
	e.leases.recs[g.id] = lease.Lease{Repo: testRepo, Group: g.id, Run: "run-1", Branch: g.branch, Worktree: g.wt, State: state}
}

// remoteRefs lists the refs the remote holds.
func (e *env) remoteRefs() string {
	return e.must(e.remote, "for-each-ref", "--format=%(refname) %(objectname)")
}

// hasBranch reports whether the clone has a local branch.
func (e *env) hasBranch(b string) bool {
	_, ok, _ := execGit{e.t}.BranchTip(context.Background(), e.clone, b)
	return ok
}
