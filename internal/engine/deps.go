package engine

import (
	"context"
	"encoding/json"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cache"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/commit"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/config"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/fault"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/lease"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// ConfigReader is the Configuration Manager as the Runtime Manager uses it.
type ConfigReader interface {
	GetConfigItem(key string) (config.Item, error)
}

// AgentLimits gives the retry count of each agent role in the registry.
type AgentLimits interface {
	Retries(agent string) (int, error)
}

// StateStore is the State Manager of one repository as the Runtime Manager uses it.
type StateStore interface {
	Load(ctx context.Context, group string) (state.Record, error)
	Create(ctx context.Context, group string, g State) (state.Record, error) // the first write of a group record
	Save(ctx context.Context, group string, g State, rev int) (state.Record, error)
}

// StateOpener returns the State Manager for the repository whose Komodo folder is under commonDir.
type StateOpener func(commonDir string) (StateStore, error)

// LeaseStore is the Lease Manager as the Runtime Manager uses it.
type LeaseStore interface {
	Grant(ctx context.Context, req lease.GrantRequest) (lease.Lease, error)
	Renew(ctx context.Context, l lease.Lease) (lease.Lease, error)
	Revoke(ctx context.Context, repo, group string, why lease.RevokeReason) error
	Holder(ctx context.Context, repo, group string) (lease.Lease, bool, error)
	IsOwner(ctx context.Context, l lease.Lease) (bool, error)
	Release(ctx context.Context, l lease.Lease) error
}

// CacheStore is the Cache Manager as the Runtime Manager uses it; only the engine opens and closes a repository's cache.
type CacheStore interface {
	Open(ctx context.Context, repo string) (*cache.Cache, error)
	Close(ctx context.Context) error
}

// Contexts is the Context Manager as the Runtime Manager uses it.
type Contexts interface {
	UseCache(repo string, h cache.Handle)
	GetContext(ctx context.Context, agent, group string) (json.RawMessage, error)
}

// TaskGroups is the Task Manager as the Runtime Manager uses it.
type TaskGroups interface {
	// Start runs one Task Manager goroutine for the group and returns its channels.
	Start(ctx context.Context, spec GroupSpec, h cache.Handle) (Channels, error)
}

// PlanSource is the Intake Handler's task lists as the Runtime Manager reads them.
type PlanSource interface {
	Specs(ctx context.Context, labels []string) ([]GroupSpec, error)
}

// EpicGit is the part of the git wrapper the Runtime Manager uses to find or create an epic's branch.
type EpicGit interface {
	BranchExists(ctx context.Context, clone, branch string) (bool, error)
	CreateBranch(ctx context.Context, clone, branch, base string) error
}

// EpicCommitter is the Commit Handler as the Runtime Manager uses it at run start.
type EpicCommitter interface {
	SaveLists(ctx context.Context, r commit.ListsRequest) error
}

// TokenCaps is the Resource Manager as the Runtime Manager uses it.
type TokenCaps interface {
	GrantGroupTokens(group string)
	EndRun(run string)
	EndGroup(group string)
}

// Discarder is the Cleanup Handler's Discard as the Runtime Manager uses it.
type Discarder interface {
	Discard(ctx context.Context, repo, group string) error
}

// CleanItem is one thing a clean removed, or would remove.
type CleanItem struct {
	Kind string `json:"kind"` // worktree, branch, container, cache or log
	Name string `json:"name"`
}

// Janitor runs the machine-wide clean; open names the repositories that still have an open run.
type Janitor interface {
	Clean(ctx context.Context, dryRun bool, open map[string]bool) ([]CleanItem, error)
}

// Extras adds the Metrics Service figures to a status answer; the keys are the StatusData JSON fields.
type Extras func(ctx context.Context, target string, all bool) map[string]any

// Printer is where the engine sends the faults that belong to a connection.
type Printer = fault.Printer

// Timing holds the durations the engine runs on; a zero field takes the constant of the same name.
type Timing struct {
	LeaseRenewEvery   time.Duration
	AgentStopGrace    time.Duration
	ShutdownDeadline  time.Duration
	PoolHalveCooldown time.Duration
	HeartbeatEvery    time.Duration
	LoopSilentFor     time.Duration
	EngineBeatEvery   time.Duration
}

// Deps are the managers the Runtime Manager and the engine call.
type Deps struct {
	Bus         bus.Bus
	Reader      bus.Reader
	Config      ConfigReader
	Agents      AgentLimits
	States      StateOpener
	Machine     *state.Machine
	Leases      LeaseStore
	Caches      CacheStore
	Contexts    Contexts
	Tasks       TaskGroups
	Plans       PlanSource
	Git         EpicGit
	Commits     EpicCommitter
	Resource    TokenCaps
	Cleaner     Discarder
	Janitor     Janitor
	Extras      Extras
	Clock       Clock
	Timing      Timing
	Version     string                             // the binary version a run is pinned to
	Home        string                             // the Komodo home folder; the worktrees live under Home/wt
	BaseBranch  string                             // the branch an epic's branch is cut from
	FreeMemGB   func() float64                     // free memory, for growing the pool; nil means the pool never grows
	KillTree    func(run, group string)            // ends every process of a group; called when a stop passes its deadline
	Go          func(func())                       // starts a goroutine the caller guards against panics; nil starts a plain one
	Errors      func(run, group string, err error) // reports an error the engine cannot act on; nil drops it
	RestartLoop func(loop string)                  // restarts a silent watcher loop in place
	Chain       func(group string) []string        // the model links of the group's current agent, in order
	Faults      *fault.Handler                     // reports a panic of a goroutine the engine starts; nil reports nothing
}

// filled returns t with every zero field replaced by its constant.
func (t Timing) filled() Timing {
	def := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&t.LeaseRenewEvery, LeaseRenewEvery)
	def(&t.AgentStopGrace, AgentStopGrace)
	def(&t.ShutdownDeadline, ShutdownDeadline)
	def(&t.PoolHalveCooldown, PoolHalveCooldown)
	def(&t.HeartbeatEvery, HeartbeatEvery)
	def(&t.LoopSilentFor, LoopSilentFor)
	def(&t.EngineBeatEvery, EngineBeatEvery)
	return t
}
