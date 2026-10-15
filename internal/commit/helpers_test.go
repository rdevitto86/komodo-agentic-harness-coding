package commit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/lease"
)

// isolate points git at a throwaway home so no test reads or writes the person's configuration.
func isolate(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	cfg := filepath.Join(home, "gitconfig")
	body := "[user]\n\tname = Test\n\temail = test@example.com\n[init]\n\tdefaultBranch = main\n[protocol \"file\"]\n\tallow = always\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("KOMODO_HOME", filepath.Join(home, ".komodo"))
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

// git runs plain git in dir for test setup and checks, and returns trimmed output.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// write creates a file under dir, with parents.
func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// world is a bare remote, a clone on main and the epic branch komodo/ep-1 on both.
type world struct {
	t      *testing.T
	ctx    context.Context
	root   string
	remote string
	clone  string
	git    *ExecGit
	traced [][]string
	mu     sync.Mutex
	// fakes
	leases *fakeLeases
	rep    *fakeReporter
	clean  *fakeCleaner
	check  *fakeChecker
	gh     *fakeGH
}

// newWorld builds the repositories and a Handler wired to fakes.
func newWorld(t *testing.T) *world {
	t.Helper()
	isolate(t)
	root := t.TempDir()
	w := &world{t: t, ctx: context.Background(), root: root}
	w.remote = filepath.Join(root, "remote.git")
	w.clone = filepath.Join(root, "clone")
	git(t, root, "init", "--bare", "-b", "main", w.remote)
	git(t, root, "init", "-b", "main", w.clone)
	write(t, w.clone, "README.md", "hello\n")
	git(t, w.clone, "add", "-A")
	git(t, w.clone, "commit", "-m", "init")
	git(t, w.clone, "remote", "add", "origin", w.remote)
	git(t, w.clone, "push", "origin", "main")
	git(t, w.clone, "fetch", "origin")
	git(t, w.clone, "remote", "set-head", "origin", "main")
	git(t, w.clone, "branch", "komodo/ep-1", "main")
	git(t, w.clone, "push", "origin", "komodo/ep-1")
	git(t, w.clone, "fetch", "origin")
	w.git = NewExecGit(w.clone)
	w.git.Trace = func(a []string) {
		w.mu.Lock()
		w.traced = append(w.traced, append([]string(nil), a...))
		w.mu.Unlock()
	}
	w.leases = &fakeLeases{m: map[string]lease.Lease{}}
	w.rep = &fakeReporter{}
	w.clean = &fakeCleaner{}
	w.check = &fakeChecker{}
	w.gh = newFakeGH(t)
	return w
}

// handler returns a Handler over the world's fakes.
func (w *world) handler() *Handler {
	return &Handler{
		Git: w.git, Forge: NewGH(w.clone), Leases: w.leases, Epics: &EpicGates{},
		Check: w.check, Report: w.rep, Cleanup: w.clean,
		Config: Settings{BaseBranch: "main", Remote: "origin"},
	}
}

// group is one group's identity and checkout.
type group struct {
	id, label, branch, wt string
	fence                 lease.Fence
}

// newGroup cuts the group branch from the epic tip, adds its worktree and takes its lease.
func (w *world) newGroup(label string) group {
	w.t.Helper()
	g := group{id: "g-" + label, label: label, branch: "komodo/ep-1-" + label, wt: filepath.Join(w.root, "wt", label), fence: 3}
	if err := w.git.AddWorktree(w.ctx, w.clone, g.wt, g.branch, "origin/komodo/ep-1"); err != nil {
		w.t.Fatal(err)
	}
	w.leases.set(g, "run-1", w.clone)
	return g
}

// work writes a file into the group's worktree.
func (w *world) work(g group, name, body string) { write(w.t, g.wt, name, body) }

// approval builds an approval of the worktree's current content.
func (w *world) approval(g group, groups ...string) Approval {
	w.t.Helper()
	tree, err := w.git.TreeOf(w.ctx, g.wt)
	if err != nil {
		w.t.Fatal(err)
	}
	return Approval{
		Run: "run-1", Group: bus.ID(g.id), Repo: "repo", Epic: "ep-1", Label: g.label, EpicGroups: groups,
		Goal: "Goal of " + g.label, Worktree: g.wt, Branch: g.branch, Fence: g.fence, Tree: tree,
		Tester:   Verdict{Tree: tree, Summary: []string{"14 tests pass"}},
		Reviewer: Verdict{Tree: tree, Summary: []string{"0 blocking findings"}},
		Facts:    PRFacts{DiffStat: "1 file changed", TasksClosed: []string{"tsk-" + g.label}},
	}
}

// remoteTip returns the commit the bare remote holds for a branch.
func (w *world) remoteTip(branch string) string {
	return git(w.t, w.remote, "rev-parse", "refs/heads/"+branch)
}

// fakeLeases serves leases from a map.
type fakeLeases struct {
	mu sync.Mutex
	m  map[string]lease.Lease
	// onRead runs before each read, so a test can change the lease between reads.
	onRead func(n int)
	reads  int
}

// set records a held lease for the group.
func (f *fakeLeases) set(g group, run, clone string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[g.id] = lease.Lease{Repo: "repo", Group: g.id, Run: run, Fence: g.fence, State: lease.StateHeld, ClonePath: clone, Worktree: g.wt, Branch: g.branch}
}

// bump raises the stored fence of a group.
func (f *fakeLeases) bump(id string, fence lease.Fence) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l := f.m[id]
	l.Fence = fence
	f.m[id] = l
}

// Holder runs the hook, then returns the stored lease.
func (f *fakeLeases) Holder(_ context.Context, _, group string) (lease.Lease, bool, error) {
	f.mu.Lock()
	f.reads++
	n := f.reads
	cb := f.onRead
	f.mu.Unlock()
	if cb != nil {
		cb(n)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.m[group]
	return l, ok, nil
}

// report is one recorded Reporter call.
type report struct {
	subtype string
	data    any
}

// fakeReporter records every report.
type fakeReporter struct {
	mu   sync.Mutex
	list []report
}

// Report records the call.
func (f *fakeReporter) Report(_ context.Context, _ bus.ID, subtype string, data any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.list = append(f.list, report{subtype, data})
	return nil
}

// subtypes lists the recorded subtypes in order.
func (f *fakeReporter) subtypes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var s []string
	for _, r := range f.list {
		s = append(s, r.subtype)
	}
	return s
}

// fakeCleaner records every Complete call.
type fakeCleaner struct {
	mu    sync.Mutex
	calls []Published
	err   error
}

// Complete records the call.
func (f *fakeCleaner) Complete(_ context.Context, p Published) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, p)
	return f.err
}

// fakeChecker serves queued recheck results; an empty queue passes.
type fakeChecker struct {
	mu      sync.Mutex
	results []CheckResult
	calls   []string
	onCall  func(epic, worktree string)
}

// Recheck pops the next queued result.
func (f *fakeChecker) Recheck(_ context.Context, epic, worktree string) (CheckResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, worktree)
	cb := f.onCall
	var r CheckResult
	if len(f.results) > 0 {
		r = f.results[0]
		f.results = f.results[1:]
	} else {
		r = CheckResult{Pass: true}
	}
	f.mu.Unlock()
	if cb != nil {
		cb(epic, worktree)
	}
	return r, nil
}

// fakeGH is a gh script on PATH that records its arguments and replays a pull request.
type fakeGH struct {
	dir string
}

// newFakeGH installs the script and puts it first on PATH.
func newFakeGH(t *testing.T) *fakeGH {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
d="$FAKE_GH_DIR"
printf '%s\n' "$*" >> "$d/calls.log"
case "$1 $2" in
"pr list")
  if [ -f "$d/pr.json" ]; then cat "$d/pr.json"; else echo "[]"; fi ;;
"pr create")
  cat > "$d/create-body.txt"
  echo '[{"url":"https://github.com/acme/shop/pull/41","headRefOid":"abc123"}]' > "$d/pr.json"
  echo "https://github.com/acme/shop/pull/41" ;;
"pr edit")
  cat > "$d/edit-body.txt" ;;
"label create") ;;
*) echo "unexpected: $*" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_GH_DIR", dir)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &fakeGH{dir: dir}
}

// calls returns the recorded argument lines.
func (f *fakeGH) calls() []string {
	b, err := os.ReadFile(filepath.Join(f.dir, "calls.log"))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// count returns how many recorded calls start with the prefix.
func (f *fakeGH) count(prefix string) int {
	n := 0
	for _, c := range f.calls() {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// file reads one file the script wrote.
func (f *fakeGH) file(name string) string {
	b, _ := os.ReadFile(filepath.Join(f.dir, name))
	return string(b)
}

// execOut runs plain git in dir and returns its output and error.
func execOut(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
