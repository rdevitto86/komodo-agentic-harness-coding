// Package sandbox seals the commands the agents run: a Docker container per group for Quality Assurance, and Seatbelt or
// Bubblewrap for the Builder. A Manager hands out instances, wraps each command so it starts inside one, and never runs
// a command unwrapped.
package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os/exec"
	"slices"
	"sync"
	"time"
)

// The two roles a sandbox instance serves.
const (
	RoleBuilder = "builder"
	RoleQA      = "qa"
)

// Spec says what one sandbox instance may read, write and reach. Writable[0] is the instance's temporary folder (HOME and
// TMPDIR); the rest are its private build caches.
type Spec struct {
	Group      string   // task group identifier
	Worktree   string   // working directory; writable except the .git entry
	GitDir     string   // this worktree's own git folder; read-only
	ReadOnly   []string // always includes <Worktree>/.git
	Writable   []string // the instance's temporary folder and its private build cache
	Modules    string   // module and registry caches; read-only
	Toolchains []string // toolchain folders; read-only
	Masked     []string // worktree files matching the secret list; unreadable
	MaxProcs   int      // sandbox.max_procs
	MaxMemGB   int      // sandbox.max_mem_gb
	Role       string   // builder or Quality Assurance; picks the mechanism
	Network    bool     // true only for the group's setup command

	SetupEnv []string // NAME=value variables only a command with Network true gets
}

// Instance is one created sandbox.
type Instance struct {
	ID      string `json:"id"`
	Group   string `json:"group"`
	Backend string `json:"backend"` // the mechanism's name
	// true when Quality Assurance runs in the operating-system sandbox because Docker is missing
	Fallback bool `json:"fallback,omitempty"`
	// why the first choice failed, when Fallback is true
	Reason    string    `json:"reason,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// newInstance returns an Instance with a fresh random id.
func newInstance(group, backend string) (Instance, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return Instance{}, fmt.Errorf("make an instance id: %w", err)
	}

	return Instance{
		ID:        hex.EncodeToString(b[:]),
		Group:     group,
		Backend:   backend,
		CreatedAt: time.Now().UTC(),
	}, nil
}

// Command is one process to start: the class's command plus the model's arguments.
type Command struct {
	Argv    []string
	Env     []string
	Timeout time.Duration
}

// Backend is one confinement mechanism.
type Backend interface {
	// Name returns the mechanism name shown to the person.
	Name() string
	// Check returns an error that names the fix when the mechanism cannot run here.
	Check() error
	// Prepare renders the rules once per instance; a container mechanism also creates the container.
	Prepare(ctx context.Context, s Spec) (Instance, error)
	// Command wraps one command so that it starts inside the instance.
	Command(ctx context.Context, i Instance, s Spec, c Command) (*exec.Cmd, error)
	// Release removes what Prepare created.
	Release(ctx context.Context, i Instance) error
}

// NetworkCutter is implemented by a backend whose network is fixed at creation and must be cut once setup ends.
type NetworkCutter interface {
	CutNetwork(ctx context.Context, i Instance) error
}

// Caveater is implemented by a backend that cannot enforce every limit a Spec asks for.
type Caveater interface {
	// Caveats names each requested limit the mechanism cannot enforce on this platform.
	Caveats(s Spec) []string
}

// StartError is the failure of Manager.Command; its text is the reply the model sees.
type StartError struct {
	Reason string
	Err    error
}

// Error returns the reply for a command the sandbox could not start.
func (e *StartError) Error() string {
	return "error: the sandbox could not start the command: " + e.Reason
}

// Unwrap returns the backend's own error.
func (e *StartError) Unwrap() error { return e.Err }

// live is one created instance with its current Spec; its lock guards the Spec.
type live struct {
	mu      sync.Mutex
	inst    Instance
	spec    Spec
	backend Backend
}

// Manager creates and destroys sandbox instances and wraps their commands.
type Manager struct {
	mu       sync.Mutex
	byRole   map[string]Backend
	fallback Backend
	live     map[string]*live
}

// NewManager builds a Manager over the backends, keyed by role ("builder" or "qa"), and the operating-system backend the
// Quality Assurance falls back to without Docker.
func NewManager(byRole map[string]Backend, qaFallback Backend) *Manager {
	return &Manager{byRole: byRole, fallback: qaFallback, live: map[string]*live{}}
}

// CreateSandboxInstance prepares one instance for a group and role.
// A Quality Assurance whose first backend fails its check runs on the fallback and the instance says so.
func (m *Manager) CreateSandboxInstance(ctx context.Context, s Spec) (Instance, error) {
	s, err := normalise(s)
	if err != nil {
		return Instance{}, err
	}

	primary, ok := m.byRole[s.Role]
	if !ok || primary == nil {
		return Instance{}, fmt.Errorf("no sandbox mechanism is set for the %s role", s.Role)
	}

	be := primary

	var (
		fallback bool
		reason   string
	)
	if cerr := primary.Check(); cerr != nil {
		if s.Role != RoleQA || m.fallback == nil {
			return Instance{}, cerr
		}
		if ferr := m.fallback.Check(); ferr != nil {
			return Instance{}, fmt.Errorf("%s; the operating-system sandbox cannot run either: %w", cerr, ferr)
		}
		be, fallback, reason = m.fallback, true, cerr.Error()
	}

	inst, err := be.Prepare(ctx, s)
	if err != nil {
		return Instance{}, err
	}

	inst.Fallback, inst.Reason = fallback, reason

	m.mu.Lock()
	if _, dup := m.live[inst.ID]; inst.ID == "" || dup {
		m.mu.Unlock()
		_ = be.Release(ctx, inst)
		return Instance{}, fmt.Errorf("the %s mechanism returned the instance id %q twice or empty", be.Name(), inst.ID)
	}

	m.live[inst.ID] = &live{inst: inst, spec: s, backend: be}
	m.mu.Unlock()
	return inst, nil
}

// DestroySandboxInstance releases an instance; destroying an unknown or destroyed instance does nothing.
func (m *Manager) DestroySandboxInstance(ctx context.Context, id string) error {
	m.mu.Lock()
	li, ok := m.live[id]
	delete(m.live, id)
	m.mu.Unlock()
	if !ok {
		return nil
	}
	return li.backend.Release(ctx, li.inst)
}

// Command wraps one command so that it starts inside the instance; the command never runs unwrapped.
func (m *Manager) Command(ctx context.Context, id string, c Command) (*exec.Cmd, error) {
	li, ok := m.get(id)
	if !ok {
		return nil, &StartError{Reason: fmt.Sprintf("instance %s does not exist or was destroyed", id)}
	}
	if len(c.Argv) == 0 {
		return nil, &StartError{Reason: "the command is empty"}
	}

	li.mu.Lock()
	spec := cloneSpec(li.spec)
	li.mu.Unlock()

	cmd, err := li.backend.Command(ctx, li.inst, spec, c)
	if err != nil {
		return nil, &StartError{Reason: err.Error(), Err: err}
	}

	return cmd, nil
}

// EndSetup turns the network off for every later command; the group's setup command is the only one that runs before it.
func (m *Manager) EndSetup(ctx context.Context, id string) error {
	li, ok := m.get(id)
	if !ok {
		return fmt.Errorf("instance %s does not exist or was destroyed", id)
	}

	li.mu.Lock()
	defer li.mu.Unlock()

	if !li.spec.Network {
		return nil
	}
	if cutter, ok := li.backend.(NetworkCutter); ok {
		if err := cutter.CutNetwork(ctx, li.inst); err != nil {
			return fmt.Errorf("cut the network of instance %s: %w", id, err)
		}
	}

	li.spec.Network = false
	return nil
}

// SetMasked replaces the unreadable file list; the Task Manager refreshes it before each command.
func (m *Manager) SetMasked(id string, masked []string) error {
	li, ok := m.get(id)
	if !ok {
		return fmt.Errorf("instance %s does not exist or was destroyed", id)
	}

	cleaned, err := cleanAll("masked file", masked)
	if err != nil {
		return err
	}

	li.mu.Lock()
	li.spec.Masked = cleaned
	li.mu.Unlock()
	return nil
}

// Caveats names the limits the instance's mechanism cannot enforce here, for the person to be told.
func (m *Manager) Caveats(id string) []string {
	li, ok := m.get(id)
	if !ok {
		return nil
	}

	cv, ok := li.backend.(Caveater)
	if !ok {
		return nil
	}

	li.mu.Lock()
	spec := cloneSpec(li.spec)
	li.mu.Unlock()
	return cv.Caveats(spec)
}

// get returns the live instance with this id.
func (m *Manager) get(id string) (*live, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	li, ok := m.live[id]
	return li, ok
}

// normalise validates a Spec and returns a copy that holds the git pointer read-only.
func normalise(s Spec) (Spec, error) {
	s = cloneSpec(s)
	r, err := newRules(s)
	if err != nil {
		return Spec{}, err
	}
	s.ReadOnly = r.ReadOnly
	return s, nil
}

// cloneSpec copies the slices of a Spec so a holder cannot change the original.
func cloneSpec(s Spec) Spec {
	s.ReadOnly = slices.Clone(s.ReadOnly)
	s.Writable = slices.Clone(s.Writable)
	s.Toolchains = slices.Clone(s.Toolchains)
	s.Masked = slices.Clone(s.Masked)
	s.SetupEnv = slices.Clone(s.SetupEnv)
	return s
}
