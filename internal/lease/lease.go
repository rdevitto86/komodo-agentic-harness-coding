// Package lease keeps the lease registry: which task group's run owns which branch and worktree.
package lease

import (
	"errors"
	"time"
)

const (
	LeaseExpiry        = 2 * time.Hour    // age of the last renewal after which a lease on another host counts as dead
	DefaultLockTimeout = 30 * time.Second // age after which a lock file is stale; the key lease.lock_timeout_s overrides it
	LockSuffix         = ".lock"          // a lease's lock file is <group-id>.lock beside its record
)

// Fence is the number that rises on every grant, takeover and revocation of a group's lease.
type Fence uint64

// LeaseState is where a lease stands in its life.
type LeaseState string

const (
	StateHeld    LeaseState = "held"    // one run holds the group
	StateRevoked LeaseState = "revoked" // ended on order; the holder is no longer the owner
)

// Lease is one registry record: the run that owns a group's branch and worktree.
type Lease struct {
	Repo      string     `json:"repo"`       // repo id
	Group     string     `json:"group"`      // task group id
	Run       string     `json:"run"`        // run that holds the group
	Branch    string     `json:"branch"`     // the group's branch name, as the caller gave it
	Worktree  string     `json:"worktree"`   // absolute path of the group's worktree
	PID       int        `json:"pid"`        // process id of the holder
	ProcStart time.Time  `json:"proc_start"` // start time of the holder's process
	Host      string     `json:"host"`       // machine the holder runs on
	ClonePath string     `json:"clone_path"` // clone the run started from
	Fence     Fence      `json:"fence"`      // fence value this holder took
	State     LeaseState `json:"state"`      // see LeaseState
	Rev       int        `json:"rev"`        // rises by 1 on every registry write
	TakenAt   time.Time  `json:"taken_at"`   // when the holder took the lease
	RenewedAt time.Time  `json:"renewed_at"` // when the holder last renewed it
}

// ProcessCheck reports whether a process with this id exists and started at the given time.
type ProcessCheck func(pid int, start time.Time) bool

var (
	ErrExists      = errors.New("lease record already exists")            // Registry.Create lost the race
	ErrStaleRev    = errors.New("lease record changed since it was read") // Registry.Swap or Delete saw a newer rev
	ErrNotFound    = errors.New("lease record does not exist")
	ErrLeased      = errors.New("group is leased by another run") // Grant: a live holder exists
	ErrLeaseLost   = errors.New("lease lost: another run took the group")
	ErrBranchTaken = errors.New("branch or worktree is owned by another group")
	ErrBadKey      = errors.New("repo or group id cannot name a file")
	ErrLocked      = errors.New("lease record stayed locked by another writer")
)

// HeldError carries the live holder so the caller can name it.
type HeldError struct{ Holder Lease }

// Error names the run that holds the group.
func (e HeldError) Error() string { return "group is leased by run " + e.Holder.Run }

// Unwrap lets errors.Is match ErrLeased.
func (e HeldError) Unwrap() error { return ErrLeased }

// Live reports whether a lease still has a working holder as seen from host.
// A holder on this host is live while its process lives; one elsewhere, while its last renewal is young.
func (l Lease) Live(now time.Time, host string, alive ProcessCheck) bool {
	if l.State != StateHeld {
		return false
	}
	if l.Host == host {
		return alive != nil && alive(l.PID, l.ProcStart)
	}
	return now.Sub(l.RenewedAt) <= LeaseExpiry
}
