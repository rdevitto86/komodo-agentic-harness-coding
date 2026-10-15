package lease

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

// grantResult is one Grant outcome a contender reports.
type grantResult struct {
	Run   string `json:"run"`
	Group string `json:"group"`
	Fence Fence  `json:"fence"` // zero when the grant failed
	Err   string `json:"err"`   // "held", "taken" or another failure
}

// classify names the outcome of a Grant error.
func classify(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrLeased):
		return "held"
	case errors.Is(err, ErrBranchTaken):
		return "taken"
	}
	return err.Error()
}

// contend runs n goroutines that each call Grant k times and returns every outcome.
// pick maps a goroutine index and call number to the group and branch it asks for.
func contend(m *Manager, tag string, n, k int, pick func(i, j int) (group, branch string), gate func()) []grantResult {
	var mu sync.Mutex
	var out []grantResult
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			gate()
			for j := 0; j < k; j++ {
				group, branch := pick(i, j)
				run := fmt.Sprintf("%s-%d", tag, i)
				l, err := m.Grant(bg, GrantRequest{
					Repo: testRepo, Group: group, Run: run, Branch: branch,
					Worktree: "/wt/" + branch, ClonePath: "/src/shop",
				})
				mu.Lock()
				out = append(out, grantResult{Run: run, Group: group, Fence: l.Fence, Err: classify(err)})
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return out
}

// wins returns the successful outcomes.
func wins(rs []grantResult) (w []grantResult) {
	for _, r := range rs {
		if r.Err == "" {
			w = append(w, r)
		}
	}
	return w
}

// assertFences checks that the winning fences of one group are exactly 1..N, each once.
func assertFences(t *testing.T, rs []grantResult, group string) {
	t.Helper()
	var f []int
	for _, r := range wins(rs) {
		if r.Group == group {
			f = append(f, int(r.Fence))
		}
	}
	sort.Ints(f)
	for i, v := range f {
		if v != i+1 {
			t.Fatalf("fences of %s are not 1..%d each once: %v", group, len(f), f)
		}
	}
}

// watchFence polls a group's record and reports any fall of the fence until stop closes.
func watchFence(t *testing.T, e *testEnv, group string, stop <-chan struct{}) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		var last Fence
		for {
			select {
			case <-stop:
				return
			default:
			}
			if l, err := e.reg.Get(bg, testRepo, group); err == nil {
				if l.Fence < last {
					t.Errorf("fence fell from %d to %d", last, l.Fence)
					return
				}
				last = l.Fence
			}
		}
	}()
	return done
}

func TestConcurrentGrantSameGroupOneOwner(t *testing.T) {
	e := newEnv(t)
	m := e.manager(100) // every goroutine shares this live process, so a second grant meets a live holder
	rs := contend(m, "g", 16, 1, func(int, int) (string, string) { return "g1", "komodo/ep-1-g1" }, func() {})
	if len(rs) != 16 {
		t.Fatal("results lost")
	}
	if w := wins(rs); len(w) != 1 || w[0].Fence != 1 {
		t.Fatalf("want one owner at fence 1: %+v", rs)
	}
}

func TestConcurrentGrantSameBranchOneOwner(t *testing.T) {
	e := newEnv(t)
	m := e.manager(100)
	rs := contend(m, "g", 16, 1, func(i, _ int) (string, string) {
		return fmt.Sprintf("g%d", i), "komodo/ep-1-shared"
	}, func() {})
	if w := wins(rs); len(w) != 1 {
		t.Fatalf("one branch must have one owner, got %d: %+v", len(w), rs)
	}
	for _, r := range rs {
		if r.Err != "" && r.Err != "taken" {
			t.Fatalf("a loser must get ErrBranchTaken: %+v", r)
		}
	}
	left, _ := e.reg.List(bg, testRepo)
	if len(left) != 1 {
		t.Fatalf("losers left records behind: %+v", left)
	}
}

func TestConcurrentGrantFencesNeverFall(t *testing.T) {
	e := newEnv(t)
	// Every holder looks dead, so each Grant is a takeover and the fence climbs by one each time.
	m := e.manager(100)
	m.alive = func(int, time.Time) bool { return false }
	stop := make(chan struct{})
	done := watchFence(t, e, "g1", stop)
	rs := contend(m, "g", 8, 10, func(int, int) (string, string) { return "g1", "komodo/ep-1-g1" }, func() {})
	close(stop)
	<-done
	if len(wins(rs)) != 80 {
		t.Fatalf("every takeover of a dead lease must succeed: %d of 80", len(wins(rs)))
	}
	assertFences(t, rs, "g1")
}

// graceful helper: waits for the barrier file so both processes start together.
func waitFor(path string) {
	for end := time.Now().Add(30 * time.Second); time.Now().Before(end); time.Sleep(time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
}

// TestGrantHelper is the child process of TestGrantAcrossProcesses; it grants in one mode and writes its outcomes to a file.
func TestGrantHelper(t *testing.T) {
	mode := os.Getenv("KOMODO_LEASE_MODE")
	if mode == "" {
		t.Skip("child of TestGrantAcrossProcesses")
	}
	id := os.Getenv("KOMODO_LEASE_ID")
	m, err := New(t.TempDir(), NewFileRegistry(os.Getenv("KOMODO_LEASE_ROOT"), DefaultLockTimeout), OSProcessCheck)
	if err != nil {
		t.Fatal(err)
	}
	var pick func(i, j int) (string, string)
	k := 1
	switch mode {
	case "same-group":
		pick = func(int, int) (string, string) { return "g1", "komodo/ep-1-g1" }
	case "same-branch":
		pick = func(i, _ int) (string, string) { return fmt.Sprintf("g-%s-%d", id, i), "komodo/ep-1-shared" }
	case "churn":
		m.alive = func(int, time.Time) bool { return false }
		pick = func(int, int) (string, string) { return "g1", "komodo/ep-1-g1" }
		k = 6
	}
	rs := contend(m, "p"+id, 4, k, pick, func() { waitFor(os.Getenv("KOMODO_LEASE_GO")) })
	b, _ := json.Marshal(rs)
	out := os.Getenv("KOMODO_LEASE_OUT")
	if err := os.WriteFile(out+".tmp", b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(out+".tmp", out); err != nil {
		t.Fatal(err)
	}
	// Stay alive until the parent has every outcome, so no holder looks dead to a slower contender.
	waitFor(os.Getenv("KOMODO_LEASE_DONE"))
}

// runChildren starts two grant helpers together in a mode and returns all their outcomes.
func runChildren(t *testing.T, root, mode string) []grantResult {
	t.Helper()
	dir := t.TempDir()
	gate := filepath.Join(dir, "go")
	var cmds []*exec.Cmd
	for _, id := range []string{"A", "B"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestGrantHelper$")
		cmd.Env = append(os.Environ(), "KOMODO_LEASE_MODE="+mode, "KOMODO_LEASE_ID="+id, "KOMODO_LEASE_ROOT="+root,
			"KOMODO_LEASE_GO="+gate, "KOMODO_LEASE_DONE="+filepath.Join(dir, "done"), "KOMODO_LEASE_OUT="+filepath.Join(dir, id+".json"))
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, cmd)
	}
	time.Sleep(300 * time.Millisecond) // let both children reach the gate
	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"A", "B"} {
		waitFor(filepath.Join(dir, id+".json"))
	}
	if err := os.WriteFile(filepath.Join(dir, "done"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatalf("child failed: %v", err)
		}
	}
	var all []grantResult
	for _, id := range []string{"A", "B"} {
		b, err := os.ReadFile(filepath.Join(dir, id+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var rs []grantResult
		if err := json.Unmarshal(b, &rs); err != nil {
			t.Fatal(err)
		}
		all = append(all, rs...)
	}
	return all
}

func TestGrantAcrossProcesses(t *testing.T) {
	t.Run("one group, two processes, one owner", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "leases")
		rs := runChildren(t, root, "same-group")
		if len(rs) != 8 {
			t.Fatalf("results lost: %d", len(rs))
		}
		if w := wins(rs); len(w) != 1 || w[0].Fence != 1 {
			t.Fatalf("want one owner at fence 1: %+v", rs)
		}
	})
	t.Run("one branch, two processes, one owner", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "leases")
		rs := runChildren(t, root, "same-branch")
		if w := wins(rs); len(w) != 1 {
			t.Fatalf("one branch must have one owner, got %d: %+v", len(w), rs)
		}
		left, err := NewFileRegistry(root, DefaultLockTimeout).List(bg, testRepo)
		if err != nil || len(left) != 1 {
			t.Fatalf("records left: %+v %v", left, err)
		}
	})
	t.Run("takeovers across two processes keep the fence rising", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "leases")
		reg := NewFileRegistry(root, DefaultLockTimeout)
		stop := make(chan struct{})
		done := make(chan struct{})
		go func() { // the parent watches the fence while the children churn it
			defer close(done)
			var last Fence
			for {
				select {
				case <-stop:
					return
				default:
				}
				if l, err := reg.Get(bg, testRepo, "g1"); err == nil {
					if l.Fence < last {
						t.Errorf("fence fell from %d to %d", last, l.Fence)
						return
					}
					last = l.Fence
				}
			}
		}()
		rs := runChildren(t, root, "churn")
		close(stop)
		<-done
		if len(wins(rs)) != 48 {
			t.Fatalf("every takeover of a dead lease must succeed: %d of 48", len(wins(rs)))
		}
		assertFences(t, rs, "g1")
		l, _ := reg.Get(bg, testRepo, "g1")
		if l.Fence != 48 {
			t.Fatalf("final fence %d", l.Fence)
		}
	})
}
