package lease

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// GrantRequest asks for a group's lease.
type GrantRequest struct {
	Repo      string // repo id
	Group     string // task group id
	Run       string // run asking
	Worktree  string // worktree path the lease records for the group
	ClonePath string // clone the run started from
}

// RevokeReason says why the Runtime Manager revoked a lease.
type RevokeReason string

const (
	RevokeStop    RevokeReason = "stop"    // a person stopped the run
	RevokeTimeout RevokeReason = "timeout" // the group ran past its time
	RevokeStalled RevokeReason = "stalled" // the holder stopped renewing
)

// Manager grants, renews, revokes and releases leases over a registry.
type Manager struct {
	reg       Registry
	alive     ProcessCheck
	machine   string // this machine's id
	pid       int    // this process
	procStart time.Time
	now       func() time.Time
	locks     sync.Map // group key to *sync.Mutex
	sems      sync.Map // repo id to the repository's semaphore
}

// retries bounds how often a call rereads after losing a race on a record's rev.
const retries = 8

// New opens the lease manager over a registry and a process check; lockTimeoutS is the value of lease.lock_timeout_s.
// A nil registry is the file registry under home/leases with that wait; a nil check is OSProcessCheck.
func New(home string, lockTimeoutS any, reg Registry, alive ProcessCheck) (*Manager, error) {
	if home == "" {
		return nil, errors.New("lease manager needs a home folder")
	}
	if reg == nil {
		reg = NewFileRegistry(filepath.Join(home, "leases"), LockTimeout(lockTimeoutS))
	}
	if alive == nil {
		alive = OSProcessCheck
	}

	id, err := machineID(home)
	if err != nil {
		return nil, err
	}

	return &Manager{
		reg:       reg,
		alive:     alive,
		machine:   id,
		pid:       os.Getpid(),
		procStart: selfStart(),
		now:       time.Now,
	}, nil
}

// Grant takes the group's lease, raises the fence and records the worktree path.
// The repository's grants run one at a time, so no two live leases can name one worktree.
func (m *Manager) Grant(ctx context.Context, req GrantRequest) (Lease, error) {
	if err := checkRequest(req); err != nil {
		return Lease{}, err
	}
	if err := ctx.Err(); err != nil {
		return Lease{}, err
	}

	unlockRepo, err := m.lockRepo(ctx, req.Repo)
	if err != nil {
		return Lease{}, err
	}

	defer unlockRepo()

	defer m.groupLock(req.Repo, req.Group)()

	for i := 0; i < retries; i++ {
		l, again, err := m.grantOnce(ctx, req)
		if !again {
			return l, err
		}
	}

	return Lease{}, fmt.Errorf("grant %s: %w", req.Group, ErrStaleRev)
}

// Renew rewrites renewed_at; ErrLeaseLost means another run holds the group.
func (m *Manager) Renew(ctx context.Context, l Lease) (Lease, error) {
	defer m.groupLock(l.Repo, l.Group)()

	for i := 0; i < retries; i++ {
		cur, err := m.reg.Get(ctx, l.Repo, l.Group)
		if errors.Is(err, ErrNotFound) {
			return Lease{}, ErrLeaseLost
		}
		if err != nil {
			return Lease{}, err
		}
		if !owns(cur, l) {
			return Lease{}, ErrLeaseLost
		}

		cur.RenewedAt = m.stamp()

		rev, err := m.reg.Swap(ctx, cur, cur.Rev)
		if errors.Is(err, ErrStaleRev) {
			continue
		}
		if errors.Is(err, ErrNotFound) {
			return Lease{}, ErrLeaseLost
		}
		if err != nil {
			return Lease{}, err
		}

		cur.Rev = rev
		return cur, nil
	}

	return Lease{}, fmt.Errorf("renew %s: %w", l.Group, ErrStaleRev)
}

// Revoke ends a live lease and raises the fence, so the holder stops being the owner.
func (m *Manager) Revoke(ctx context.Context, repo, group string, why RevokeReason) error {
	switch why {
	case RevokeStop, RevokeTimeout, RevokeStalled:
	default:
		return fmt.Errorf("unknown revoke reason %q", why)
	}

	unlockRepo, err := m.lockRepo(ctx, repo)
	if err != nil {
		return err
	}

	defer unlockRepo()

	defer m.groupLock(repo, group)()

	for i := 0; i < retries; i++ {
		cur, err := m.reg.Get(ctx, repo, group)
		if err != nil {
			return err
		}
		if cur.State == StateRevoked {
			return nil
		}

		high, err := m.highFence(ctx, repo, group)
		if err != nil {
			return err
		}

		cur.State, cur.Fence = StateRevoked, max(cur.Fence, high)+1
		if err := m.raiseFence(ctx, repo, group, cur.Fence); err != nil {
			return err
		}
		if _, err := m.reg.Swap(ctx, cur, cur.Rev); !errors.Is(err, ErrStaleRev) {
			return err
		}
	}

	return fmt.Errorf("revoke %s: %w", group, ErrStaleRev)
}

// Holder reads the lease of a group; the bool is false when the group has none.
func (m *Manager) Holder(ctx context.Context, repo, group string) (Lease, bool, error) {
	l, err := m.reg.Get(ctx, repo, group)
	if errors.Is(err, ErrNotFound) {
		return Lease{}, false, nil
	}
	return l, err == nil, err
}

// IsOwner reports whether the lease still names this run and fence in the registry.
func (m *Manager) IsOwner(ctx context.Context, l Lease) (bool, error) {
	defer m.groupLock(l.Repo, l.Group)()

	cur, err := m.reg.Get(ctx, l.Repo, l.Group)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	return owns(cur, l), nil
}

// Release deletes the group's record. It refuses to delete a record another run holds.
func (m *Manager) Release(ctx context.Context, l Lease) error {
	defer m.groupLock(l.Repo, l.Group)()

	for i := 0; i < retries; i++ {
		cur, err := m.reg.Get(ctx, l.Repo, l.Group)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if cur.Run != l.Run || (cur.State == StateHeld && cur.Fence != l.Fence) {
			return ErrLeaseLost
		}

		err = m.reg.Delete(ctx, l.Repo, l.Group, cur.Rev)
		if errors.Is(err, ErrStaleRev) {
			continue
		}
		if errors.Is(err, ErrNotFound) {
			return nil
		}

		return err
	}

	return fmt.Errorf("release %s: %w", l.Group, ErrStaleRev)
}

// Live reports whether a lease still has a working holder: a running process on this machine, or a recent renewal elsewhere.
func (m *Manager) Live(l Lease) bool { return l.Live(time.Now(), m.machine, m.alive) }

// groupLock locks one group inside this process and returns the unlock.
func (m *Manager) groupLock(repo, group string) func() {
	mu := m.mutex("g\x00" + repo + "\x00" + group)
	mu.Lock()
	return mu.Unlock
}

// mutex returns the in-process lock for a key, creating it on first use.
func (m *Manager) mutex(key string) *sync.Mutex {
	mu, _ := m.locks.LoadOrStore(key, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// lockRepo holds one repository's lock inside this process and, when the registry offers it,
// across processes.
// Waiting for either ends when the context does.
func (m *Manager) lockRepo(ctx context.Context, repo string) (func(), error) {
	sem := m.repoSem(repo)
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	rl, ok := m.reg.(RepoLocker)
	if !ok {
		return func() { <-sem }, nil
	}

	unlock, err := rl.LockRepo(ctx, repo)
	if err != nil {
		<-sem
		return nil, err
	}

	return func() { unlock(); <-sem }, nil
}

// repoSem returns the in-process semaphore of a repository, creating it on first use.
func (m *Manager) repoSem(repo string) chan struct{} {
	sem, _ := m.sems.LoadOrStore(repo, make(chan struct{}, 1))
	return sem.(chan struct{})
}

// grantOnce tries one grant; the bool is true when a lost race means the caller should reread and try again.
func (m *Manager) grantOnce(ctx context.Context, req GrantRequest) (Lease, bool, error) {
	now := m.stamp()

	all, err := m.reg.List(ctx, req.Repo)
	if err != nil {
		return Lease{}, false, err
	}

	for _, o := range all {
		if o.Group != req.Group &&
			o.Worktree == req.Worktree &&
			o.Live(now, m.machine, m.alive) {
			return Lease{}, false, ErrWorktreeTaken
		}
	}

	high, err := m.highFence(ctx, req.Repo, req.Group)
	if err != nil {
		return Lease{}, false, err
	}

	next := Lease{
		Repo:      req.Repo,
		Group:     req.Group,
		Run:       req.Run,
		Worktree:  req.Worktree,
		PID:       m.pid,
		ProcStart: m.procStart.UTC().Truncate(time.Second),
		Host:      m.machine,
		ClonePath: req.ClonePath,
		Fence:     high + 1,
		State:     StateHeld,
		TakenAt:   now,
		RenewedAt: now,
	}

	cur, err := m.reg.Get(ctx, req.Repo, req.Group)
	if errors.Is(err, ErrNotFound) {
		next.Rev = 1
		if err := m.raiseFence(ctx, req.Repo, req.Group, next.Fence); err != nil {
			return Lease{}, false, err
		}
		err = m.reg.Create(ctx, next)
		return next, errors.Is(err, ErrExists), exceptExists(err)
	}
	if err != nil {
		return Lease{}, false, err
	}
	if cur.Live(now, m.machine, m.alive) {
		return Lease{}, false, HeldError{Holder: cur}
	}

	next.Fence = max(cur.Fence, high) + 1
	if err := m.raiseFence(ctx, req.Repo, req.Group, next.Fence); err != nil {
		return Lease{}, false, err
	}

	rev, err := m.reg.Swap(ctx, next, cur.Rev)
	if errors.Is(err, ErrStaleRev) || errors.Is(err, ErrNotFound) {
		return Lease{}, true, nil
	}

	next.Rev = rev
	return next, false, err
}

// stamp returns the current time in UTC to the second, the form the records hold.
func (m *Manager) stamp() time.Time { return m.now().UTC().Truncate(time.Second) }

// highFence returns the highest fence a group ever had, or 0 when the registry keeps none.
func (m *Manager) highFence(ctx context.Context, repo, group string) (Fence, error) {
	if fk, ok := m.reg.(FenceKeeper); ok {
		return fk.HighFence(ctx, repo, group)
	}
	return 0, nil
}

// raiseFence records a fence in the registry's high-water mark before the record that carries it
// is written.
func (m *Manager) raiseFence(ctx context.Context, repo, group string, f Fence) error {
	if fk, ok := m.reg.(FenceKeeper); ok {
		return fk.RaiseFence(ctx, repo, group, f)
	}
	return nil
}

// checkRequest refuses a grant request that cannot be recorded.
func checkRequest(req GrantRequest) error {
	switch {
	case req.Repo == "" || req.Group == "" || req.Run == "":
		return errors.New("grant needs a repo, a group and a run")
	case !filepath.IsAbs(req.Worktree):
		return errors.New("grant needs an absolute worktree path")
	case !validGroup(req.Group):
		return fmt.Errorf("%w: group %q", ErrBadKey, req.Group)
	}

	return nil
}

// exceptExists hides ErrExists, which grantOnce answers by rereading.
func exceptExists(err error) error {
	if errors.Is(err, ErrExists) {
		return nil
	}
	return err
}

// owns reports whether a record is the held lease that l names.
func owns(rec, l Lease) bool {
	return rec.State == StateHeld && rec.Run == l.Run && rec.Fence == l.Fence
}
