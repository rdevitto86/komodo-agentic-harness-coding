package lease

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const testRepo = "github.com/acme/shop"

var testStart = time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)

// procTable is a fake process table: a process is alive while its pid maps to its start time.
type procTable struct {
	mu sync.Mutex
	p  map[int]time.Time
}

// newProcs returns an empty table.
func newProcs() *procTable { return &procTable{p: map[int]time.Time{}} }

// check is the ProcessCheck over the table.
func (pt *procTable) check(pid int, start time.Time) bool {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	s, ok := pt.p[pid]
	return ok && s.Equal(start)
}

// start adds a live process.
func (pt *procTable) start(pid int, at time.Time) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	pt.p[pid] = at
}

// kill removes a process.
func (pt *procTable) kill(pid int) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	delete(pt.p, pid)
}

// clock is a fake clock the tests move by hand.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

// now returns the fake time.
func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// advance moves the fake time forward.
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// testEnv is one registry folder, a process table and a clock that several managers share.
type testEnv struct {
	t     *testing.T
	root  string
	reg   *FileRegistry
	procs *procTable
	clk   *clock
}

// newEnv builds the shared parts over a temporary folder.
func newEnv(t *testing.T) *testEnv {
	t.Helper()
	root := filepath.Join(t.TempDir(), "leases")
	return &testEnv{
		t:     t,
		root:  root,
		reg:   NewFileRegistry(root, DefaultLockTimeout),
		procs: newProcs(),
		clk:   &clock{t: testStart},
	}
}

// manager returns a manager that acts as the process with this pid on host h1.
func (e *testEnv) manager(pid int) *Manager {
	e.t.Helper()
	m, err := New(e.t.TempDir(), nil, e.reg, e.procs.check)
	if err != nil {
		e.t.Fatalf("New: %v", err)
	}
	m.machine, m.pid, m.procStart, m.now = "h1", pid, testStart, e.clk.now
	e.procs.start(pid, testStart)
	return m
}

// recordFile returns the path of a lease's record file.
func recordFile(t *testing.T, r *FileRegistry, repo, group string) string {
	t.Helper()
	_, p, _, err := r.paths(repo, group)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// lockFile returns the path of a lease's lock file.
func lockFile(t *testing.T, r *FileRegistry, repo, group string) string {
	t.Helper()
	_, _, p, err := r.paths(repo, group)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// req builds a grant request for a group on its own worktree.
func req(run, group string) GrantRequest {
	return GrantRequest{
		Repo: testRepo, Group: group, Run: run,
		Worktree:  "/wt/" + group,
		ClonePath: "/src/shop",
	}
}
