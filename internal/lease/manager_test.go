package lease

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

var bg = context.Background()

func TestNewDefaults(t *testing.T) {
	home := t.TempDir()
	m, err := New(home, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	l, err := m.Grant(bg, req("run-a", "g1"))
	if err != nil {
		t.Fatalf("Grant over the default registry: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "leases")); err != nil {
		t.Fatalf("the default registry lives under home/leases: %v", err)
	}
	if ok, err := m.IsOwner(bg, l); err != nil || !ok {
		t.Fatalf("IsOwner with the real process check: %v %v", ok, err)
	}
	if l.PID != os.Getpid() || l.Host == "" || l.ProcStart.IsZero() {
		t.Fatalf("holder identity not filled: %+v", l)
	}
}

func TestGrantFirst(t *testing.T) {
	e := newEnv(t)
	m := e.manager(100)
	l, err := m.Grant(bg, req("run-a", "g1"))
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if l.Fence != 1 || l.State != StateHeld || l.Rev != 1 || l.Run != "run-a" {
		t.Fatalf("lease %+v", l)
	}
	if l.Branch != "komodo/ep-1-g1" || l.Worktree != "/wt/g1" || l.ClonePath != "/src/shop" {
		t.Fatalf("names not recorded as given: %+v", l)
	}
	if l.PID != 100 || l.Host != "h1" || !l.ProcStart.Equal(testStart) {
		t.Fatalf("holder identity: %+v", l)
	}
	if !l.TakenAt.Equal(testStart) || !l.RenewedAt.Equal(testStart) {
		t.Fatalf("times: %+v", l)
	}
	got, ok, err := m.Holder(bg, testRepo, "g1")
	if err != nil || !ok || got != l {
		t.Fatalf("Holder: %+v %v %v", got, ok, err)
	}
	free, err := core.FileLock(bg, lockFile(t, e.reg, testRepo, "g1"), 0)
	if err != nil {
		t.Fatalf("no writer is left holding the lock: %v", err)
	}
	_ = free()
}

func TestGrantRejectsBadRequests(t *testing.T) {
	m := newEnv(t).manager(100)
	for name, mod := range map[string]func(*GrantRequest){
		"no branch":        func(r *GrantRequest) { r.Branch = "" },
		"no worktree":      func(r *GrantRequest) { r.Worktree = "" },
		"relative tree":    func(r *GrantRequest) { r.Worktree = "wt/g1" },
		"no run":           func(r *GrantRequest) { r.Run = "" },
		"group with slash": func(r *GrantRequest) { r.Group = "../x" },
	} {
		r := req("run-a", "g1")
		mod(&r)
		if _, err := m.Grant(bg, r); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestSecondGrantReturnsHeldError(t *testing.T) {
	e := newEnv(t)
	a, b := e.manager(100), e.manager(200)
	first, err := a.Grant(bg, req("run-a", "g1"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.Grant(bg, req("run-b", "g1"))
	var he HeldError
	if !errors.As(err, &he) || !errors.Is(err, ErrLeased) {
		t.Fatalf("want HeldError, got %v", err)
	}
	if he.Holder.Run != "run-a" || he.Holder.Fence != first.Fence {
		t.Fatalf("holder named: %+v", he.Holder)
	}
}

func TestTakeoverOfDeadHolder(t *testing.T) {
	e := newEnv(t)
	a, b := e.manager(100), e.manager(200)
	old, err := a.Grant(bg, req("run-a", "g1"))
	if err != nil {
		t.Fatal(err)
	}
	e.procs.kill(100)
	got, err := b.Grant(bg, req("run-b", "g1"))
	if err != nil {
		t.Fatalf("takeover: %v", err)
	}
	if got.Fence != old.Fence+1 || got.Run != "run-b" || got.Rev != old.Rev+1 {
		t.Fatalf("takeover lease %+v", got)
	}
	if ok, _ := a.IsOwner(bg, old); ok {
		t.Fatal("the old holder must stop being the owner")
	}
	if _, err := a.Renew(bg, old); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("Renew after takeover: %v", err)
	}
	if ok, _ := b.IsOwner(bg, got); !ok {
		t.Fatal("the new holder must be the owner")
	}
}

func TestSameHostHolderNeverTakenForAge(t *testing.T) {
	e := newEnv(t)
	a, b := e.manager(100), e.manager(200)
	if _, err := a.Grant(bg, req("run-a", "g1")); err != nil {
		t.Fatal(err)
	}
	e.clk.advance(3 * time.Hour)
	if _, err := b.Grant(bg, req("run-b", "g1")); !errors.Is(err, ErrLeased) {
		t.Fatalf("a live same-host process must keep its lease: %v", err)
	}
}

func TestRemoteHostTakeoverAfterExpiry(t *testing.T) {
	e := newEnv(t)
	a, b := e.manager(100), e.manager(200)
	a.machine = "h2"
	old, err := a.Grant(bg, req("run-a", "g1"))
	if err != nil {
		t.Fatal(err)
	}
	e.clk.advance(LeaseExpiry)
	if _, err := b.Grant(bg, req("run-b", "g1")); !errors.Is(err, ErrLeased) {
		t.Fatalf("a remote lease exactly at the expiry is still live: %v", err)
	}
	e.clk.advance(time.Minute)
	got, err := b.Grant(bg, req("run-b", "g1"))
	if err != nil || got.Fence != old.Fence+1 {
		t.Fatalf("takeover of an expired remote lease: %+v %v", got, err)
	}
}

func TestRaceForDeadLeaseYieldsOneHolder(t *testing.T) {
	e := newEnv(t)
	first, err := e.manager(100).Grant(bg, req("run-0", "g1"))
	if err != nil {
		t.Fatal(err)
	}
	e.procs.kill(100)
	var mgrs []*Manager
	for i := 0; i < 12; i++ {
		mgrs = append(mgrs, e.manager(300+i))
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var won []Lease
	var other []error
	for i, m := range mgrs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, err := m.Grant(bg, req("run-"+string(rune('a'+i)), "g1"))
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				won = append(won, l)
			} else {
				other = append(other, err)
			}
		}()
	}
	wg.Wait()
	if len(won) != 1 || won[0].Fence != first.Fence+1 {
		t.Fatalf("want one holder at fence %d, got %+v", first.Fence+1, won)
	}
	for _, err := range other {
		if !errors.Is(err, ErrLeased) {
			t.Errorf("loser got %v", err)
		}
	}
}

func TestRenew(t *testing.T) {
	e := newEnv(t)
	m := e.manager(100)
	l, _ := m.Grant(bg, req("run-a", "g1"))
	e.clk.advance(5 * time.Minute)
	r, err := m.Renew(bg, l)
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if !r.RenewedAt.Equal(testStart.Add(5*time.Minute)) || !r.TakenAt.Equal(l.TakenAt) || r.Fence != l.Fence ||
		r.Rev != l.Rev+1 {
		t.Fatalf("renewal changes renewed_at only: %+v", r)
	}
	if ok, _ := m.IsOwner(bg, r); !ok {
		t.Fatal("still the owner after renewing")
	}
	// An older copy of the lease (lower rev) still renews: the check is on run and fence.
	if _, err := m.Renew(bg, l); err != nil {
		t.Fatalf("Renew with an earlier copy of the same lease: %v", err)
	}
	if _, err := m.Renew(bg, Lease{Repo: testRepo, Group: "missing", Run: "x"}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("Renew of a group with no record: %v", err)
	}
}

func TestRevokeThenRenewAndRegrant(t *testing.T) {
	e := newEnv(t)
	m := e.manager(100)
	l, _ := m.Grant(bg, req("run-a", "g1"))
	if err := m.Revoke(bg, testRepo, "g1", RevokeStalled); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	h, ok, _ := m.Holder(bg, testRepo, "g1")
	if !ok || h.State != StateRevoked || h.Fence != l.Fence+1 {
		t.Fatalf("revoked record: %+v", h)
	}
	if _, err := m.Renew(bg, l); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("Renew after Revoke: %v", err)
	}
	if ok, _ := m.IsOwner(bg, l); ok {
		t.Fatal("IsOwner must be false for a revoked lease")
	}
	if err := m.Revoke(bg, testRepo, "g1", RevokeStop); err != nil {
		t.Fatalf("revoking a revoked lease: %v", err)
	}
	if h2, _, _ := m.Holder(bg, testRepo, "g1"); h2.Fence != h.Fence || h2.Rev != h.Rev {
		t.Fatalf("a second revoke must change nothing: %+v", h2)
	}
	again, err := m.Grant(bg, req("run-a", "g1"))
	if err != nil || again.Fence != l.Fence+2 {
		t.Fatalf("Grant after Revoke: %+v %v", again, err)
	}
	if err := m.Revoke(bg, testRepo, "none", RevokeStop); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Revoke of an absent group: %v", err)
	}
	if err := m.Revoke(bg, testRepo, "g1", "because"); err == nil {
		t.Fatal("an unknown reason must be refused")
	}
}

func TestIsOwner(t *testing.T) {
	e := newEnv(t)
	m := e.manager(100)
	l, _ := m.Grant(bg, req("run-a", "g1"))
	if ok, err := m.IsOwner(bg, l); err != nil || !ok {
		t.Fatalf("the granted lease owns: %v %v", ok, err)
	}
	low := l
	low.Fence = 0
	if ok, _ := m.IsOwner(bg, low); ok {
		t.Fatal("a lower fence must not own")
	}
	other := l
	other.Run = "run-z"
	if ok, _ := m.IsOwner(bg, other); ok {
		t.Fatal("another run must not own")
	}
	gone := l
	gone.Group = "none"
	if ok, err := m.IsOwner(bg, gone); ok || err != nil {
		t.Fatalf("no record: %v %v", ok, err)
	}
	before, _ := e.reg.Get(bg, testRepo, "g1")
	_, _ = m.IsOwner(bg, l)
	after, _ := e.reg.Get(bg, testRepo, "g1")
	if before != after {
		t.Fatal("IsOwner must write nothing")
	}
}

func TestBranchAndWorktreeTaken(t *testing.T) {
	e := newEnv(t)
	a, b := e.manager(100), e.manager(200)
	if _, err := a.Grant(bg, req("run-a", "g1")); err != nil {
		t.Fatal(err)
	}
	sameBranch := req("run-b", "g2")
	sameBranch.Branch = "komodo/ep-1-g1"
	if _, err := b.Grant(bg, sameBranch); !errors.Is(err, ErrBranchTaken) {
		t.Fatalf("same branch: %v", err)
	}
	sameTree := req("run-b", "g2")
	sameTree.Worktree = "/wt/g1"
	if _, err := b.Grant(bg, sameTree); !errors.Is(err, ErrBranchTaken) {
		t.Fatalf("same worktree: %v", err)
	}
	if _, err := e.reg.Get(bg, testRepo, "g2"); !errors.Is(err, ErrNotFound) {
		t.Fatal("a refused grant must leave no record")
	}
	// A dead holder's branch is free again.
	e.procs.kill(100)
	if _, err := b.Grant(bg, sameBranch); err != nil {
		t.Fatalf("branch of a dead holder: %v", err)
	}
	// So is a revoked one.
	if err := b.Revoke(bg, testRepo, "g2", RevokeStop); err != nil {
		t.Fatal(err)
	}
	c := e.manager(300)
	if _, err := c.Grant(bg, req("run-c", "g3")); err != nil {
		t.Fatalf("unrelated group: %v", err)
	}
	reuse := req("run-c", "g4")
	reuse.Branch = "komodo/ep-1-g1"
	if _, err := c.Grant(bg, reuse); err != nil {
		t.Fatalf("branch of a revoked lease: %v", err)
	}
}

func TestPausedGroupKeepsItsLease(t *testing.T) {
	e := newEnv(t)
	a, b := e.manager(100), e.manager(200)
	l, _ := a.Grant(bg, req("run-a", "g1"))
	for i := 0; i < 40; i++ { // 40 renewals, 5 minutes apart: past LeaseExpiry
		e.clk.advance(5 * time.Minute)
		var err error
		if l, err = a.Renew(bg, l); err != nil {
			t.Fatalf("renewal %d: %v", i, err)
		}
	}
	h, _, _ := a.Holder(bg, testRepo, "g1")
	if h.State != StateHeld || h.Fence != 1 {
		t.Fatalf("a paused group stays held at the same fence: %+v", h)
	}
	if _, err := b.Grant(bg, req("run-b", "g1")); !errors.Is(err, ErrLeased) {
		t.Fatalf("others cannot take a paused group: %v", err)
	}
}

func TestRelease(t *testing.T) {
	e := newEnv(t)
	a, b := e.manager(100), e.manager(200)
	l, _ := a.Grant(bg, req("run-a", "g1"))
	e.procs.kill(100)
	l2, _ := b.Grant(bg, req("run-b", "g1"))
	if err := a.Release(bg, l); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("releasing a lease another run holds: %v", err)
	}
	if _, ok, _ := b.Holder(bg, testRepo, "g1"); !ok {
		t.Fatal("a lost lease's Release must not delete the new holder's record")
	}
	if err := b.Release(bg, l2); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, ok, err := b.Holder(bg, testRepo, "g1"); ok || err != nil {
		t.Fatalf("record after Release: %v %v", ok, err)
	}
	if err := b.Release(bg, l2); err != nil {
		t.Fatalf("a second Release is a no-op: %v", err)
	}
	again, err := b.Grant(bg, req("run-b", "g1"))
	if err != nil || again.Fence != l2.Fence+1 {
		t.Fatalf("Grant after Release starts above the highest fence the group had: %+v %v", again, err)
	}
}

func TestReleaseOfRevokedLease(t *testing.T) {
	e := newEnv(t)
	m := e.manager(100)
	l, _ := m.Grant(bg, req("run-a", "g1"))
	if err := m.Revoke(bg, testRepo, "g1", RevokeTimeout); err != nil {
		t.Fatal(err)
	}
	if err := m.Release(bg, l); err != nil {
		t.Fatalf("the run that was revoked closes its group: %v", err)
	}
	if _, ok, _ := m.Holder(bg, testRepo, "g1"); ok {
		t.Fatal("record must be gone")
	}
}

func TestSweep(t *testing.T) {
	e := newEnv(t)
	clone := t.TempDir()
	a, b, c := e.manager(100), e.manager(200), e.manager(300)
	gone := filepath.Join(t.TempDir(), "deleted-clone")

	live := req("run-a", "live")
	live.ClonePath = clone
	if _, err := a.Grant(bg, live); err != nil {
		t.Fatal(err)
	}
	dead := req("run-b", "dead")
	dead.ClonePath = gone
	if _, err := b.Grant(bg, dead); err != nil {
		t.Fatal(err)
	}
	e.procs.kill(200)
	liveGone := req("run-c", "livegone")
	liveGone.ClonePath = gone
	if _, err := c.Grant(bg, liveGone); err != nil {
		t.Fatal(err)
	}
	rep, err := a.Sweep(bg, testRepo)
	if err != nil || rep.LeasesDropped != 1 {
		t.Fatalf("Sweep: %+v %v", rep, err)
	}
	if _, ok, _ := a.Holder(bg, testRepo, "dead"); ok {
		t.Fatal("a dead lease whose clone is gone must be dropped")
	}
	for _, g := range []string{"live", "livegone"} {
		if _, ok, _ := a.Holder(bg, testRepo, g); !ok {
			t.Fatalf("the sweep touched the live lease %s", g)
		}
	}
	if rep, _ := a.Sweep(bg, testRepo); rep.LeasesDropped != 0 {
		t.Fatalf("second sweep: %+v", rep)
	}
}

func TestCancelledContext(t *testing.T) {
	m := newEnv(t).manager(100)
	ctx, cancel := context.WithCancel(bg)
	cancel()
	if _, err := m.Grant(ctx, req("run-a", "g1")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Grant with a cancelled context: %v", err)
	}
}

// The lock timeout key reaches the default file registry as the longest lock wait.
func TestNewWiresTheLockTimeout(t *testing.T) {
	for _, c := range []struct {
		in   any
		want time.Duration
	}{{7, 7 * time.Second}, {nil, DefaultLockTimeout}, {-1, DefaultLockTimeout}} {
		m, err := New(t.TempDir(), c.in, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := m.reg.(*FileRegistry).lockTimeout; got != c.want {
			t.Errorf("lock_timeout_s %v: wait %v, want %v", c.in, got, c.want)
		}
	}
}

// Release then Grant gives fence 3 after fence 2: the fence never falls, even with no record left.
func TestFenceSurvivesRelease(t *testing.T) {
	e := newEnv(t)
	m := e.manager(100)
	l1, err := m.Grant(bg, req("run-a", "g1"))
	if err != nil || l1.Fence != 1 {
		t.Fatalf("first grant: %+v %v", l1, err)
	}
	if err := m.Revoke(bg, testRepo, "g1", RevokeStop); err != nil {
		t.Fatal(err)
	}
	cur, _, _ := m.Holder(bg, testRepo, "g1")
	if cur.Fence != 2 {
		t.Fatalf("revoke should raise the fence to 2: %+v", cur)
	}
	if err := m.Release(bg, l1); err != nil {
		t.Fatal(err)
	}
	l3, err := m.Grant(bg, req("run-b", "g1"))
	if err != nil || l3.Fence != 3 {
		t.Fatalf("grant after release: want fence 3, got %+v %v", l3, err)
	}
	other, err := m.Grant(bg, req("run-c", "g2"))
	if err != nil || other.Fence != 1 {
		t.Fatalf("another group keeps its own count: %+v %v", other, err)
	}
	if _, err := os.Stat(filepath.Join(e.root, "github.com%2Facme%2Fshop", ".fences.json")); err != nil {
		t.Fatalf("the high-water file is missing: %v", err)
	}
}
