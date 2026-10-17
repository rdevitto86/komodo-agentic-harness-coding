package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cli"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/lease"
	"os"
	"path"
	"path/filepath"
	"sync"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cache"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cleanup"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/commit"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/contextmgr"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/intake"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/metrics"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/resource"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// pauseProxy gives the Resource Manager the Runtime Manager that is built after it.
type pauseProxy struct{ m *engine.Manager }

// repoKit holds the parts bound to one repository: its context, commit, cleanup and intake
// handlers.
type repoKit struct {
	s                *stack
	id, root, common string
	cm               *contextmgr.Manager
	st               *state.Manager
	landings         string

	cleanup *cleanup.Handler
	commit  *commit.Handler
	intake  *intake.Handler
}

// lazyCache is the repository's cache handle, resolved through the store on every call so a cache
// the engine closed and reopened is never stale.
type lazyCache struct {
	store *cacheStore
	repo  string
}

// hub routes the Runtime Manager's calls to the kit of the repository they name.
type hub struct{ s *stack }

// cacheStore opens and closes the cache of each repository.
type cacheStore struct {
	home, version string
	mu            sync.Mutex
	open          map[string]*cache.Cache
}

// PauseGroups pauses the named groups.
func (p *pauseProxy) PauseGroups(ctx context.Context, o resource.PauseOrder) error {
	return p.m.PauseGroups(ctx, o)
}

// NudgeGroups sends the token-cap nudge.
func (p *pauseProxy) NudgeGroups(ctx context.Context, o resource.NudgeOrder) error {
	return p.m.NudgeGroups(ctx, o)
}

// landingPath is where the Commit Handler keeps an epic's landing worktree.
func (k *repoKit) landingPath(epic string) string {
	return filepath.Join(k.landings, epic)
}

// Key hashes the inputs into an entry key.
func (l lazyCache) Key(inputs ...string) cache.Key {
	c, err := l.store.Open(context.Background(), l.repo)
	if err != nil {
		return ""
	}

	return c.Handle().Key(inputs...)
}

// Get returns the payload under key.
func (l lazyCache) Get(ns cache.Namespace, key cache.Key) ([]byte, bool) {
	c, err := l.store.Open(context.Background(), l.repo)
	if err != nil {
		return nil, false
	}

	return c.Handle().Get(ns, key)
}

// Put stores data under key.
func (l lazyCache) Put(ns cache.Namespace, key cache.Key, data []byte) error {
	c, err := l.store.Open(context.Background(), l.repo)
	if err != nil {
		return err
	}

	return c.Handle().Put(ns, key, data)
}

// Start starts the Task Manager of a group in its repository's kit.
func (h *hub) Start(
	ctx context.Context,
	spec engine.GroupSpec,
	c cache.Handle,
) (engine.Channels, error) {
	k, err := h.s.kitFor(spec.Root, "", spec.Repo)
	if err != nil {
		return engine.Channels{}, err
	}

	return k.start(ctx, spec, c)
}

// SaveLists commits an epic's task lists in the run's repository.
func (h *hub) SaveLists(ctx context.Context, r commit.ListsRequest) error {
	k, err := h.s.kitFor(r.Root, r.CommonDir, r.Repo)
	if err != nil {
		return err
	}

	return k.commit.SaveLists(ctx, r)
}

// Discard frees a group's resources in the repository that holds it; the engine's own lease on the
// group does not stop it.
func (h *hub) Discard(ctx context.Context, repo, root, group string) error {
	k, err := h.s.kitFor(root, "", repo)
	if err != nil {
		return err
	}

	cp := *k.cleanup
	cp.Task = h.s.refFor(group)
	cp.Live = func(
		l lease.Lease,
	) bool {
		return (l.Group != group || l.PID != os.Getpid()) && h.s.leases.Live(l)
	}
	_, err = cp.Discard(ctx, group, cleanup.DiscardOptions{Reason: "abandon"})

	return err
}

// Clean sweeps the repository the request names, every repository a lease leaves behind, and every
// repository this engine has served.
func (h *hub) Clean(
	ctx context.Context,
	dryRun bool,
	root string,
	_ map[string]bool,
) ([]engine.CleanItem, error) {
	roots := map[string]string{}
	if root != "" {
		roots[root] = ""
	}

	for r, id := range h.s.leaseRepos() {
		roots[r] = id
	}

	for r, id := range roots {
		_, _ = h.s.kitFor(r, "", id)
	}

	h.s.kitMu.Lock()

	kits := make([]*repoKit, 0, len(h.s.kits))
	for _, k := range h.s.kits {
		kits = append(kits, k)
	}
	h.s.kitMu.Unlock()

	var out []engine.CleanItem

	for _, k := range kits {
		items, err := janitor{k.cleanup}.Clean(ctx, dryRun, nil)
		if err != nil {
			return out, err
		}

		out = append(out, items...)
	}

	return out, nil
}

// Open opens the repository's cache once and returns it; a cache closed earlier is opened again.
func (c *cacheStore) Open(_ context.Context, repo string) (*cache.Cache, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if ch := c.open[repo]; ch != nil {
		return ch, nil
	}

	ch, err := cache.New(
		filepath.Join(c.home, "cache", repo),
		c.version,
		cache.Limits{LangRoot: filepath.Join(c.home, "cache", "lang")},
	)
	if err != nil {
		return nil, err
	}

	c.open[repo] = ch

	return ch, nil
}

// Close closes every open cache.
func (c *cacheStore) Close(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var errs []error
	for _, ch := range c.open {
		errs = append(errs, ch.Close())
	}

	c.open = map[string]*cache.Cache{}

	return errors.Join(errs...)
}

// buildEngine wires the Runtime Manager to every part it calls and starts the watcher loops.
func (s *stack) buildEngine(ctx context.Context) (*engine.Engine, error) {
	s.ctx = ctx
	h := &hub{s: s}
	px := &pauseProxy{}
	res := resource.New(
		s.thresholds(),
		resource.Deps{
			Bus:     s.bus,
			Errors:  s.faults,
			Runtime: px,
			State:   resource.NewRecords(s.machine),
		},
	)
	d := engine.Deps{
		Bus: s.bus, Reader: s.reader, Config: s.cfg, Agents: agentRetries{s.cfg},
		States:   stateOpener,
		Machine:  s.machine,
		Leases:   s.leases,
		Caches:   s.caches,
		Contexts: ctxAdapter{},
		Tasks:    h, Plans: planSource{}, Git: epicGit{s.git}, Commits: h, Resource: res,
		Version: version, Home: s.home, BaseBranch: s.baseBranch(), Faults: s.faults,
		Cleaner: h, Janitor: h,
		Errors: func(run, group string, err error) { s.logln("komodo engine:", run, group, err) },
	}
	eng := engine.NewEngine(d, filepath.Join(s.home, "state", engine.SocketName))
	s.mgr = eng.Manager()
	px.m = eng.Manager()

	s.spawn(func() { _ = res.Run(ctx) })

	if ms, err := metrics.New(
		s.bus,
		filepath.Join(s.home, "metrics"),
		metrics.Options{},
		nil,
	); err == nil {
		s.spawn(func() { _ = ms.Run(ctx) })
	}

	s.spawn(func() { s.routePlans(ctx) })
	s.spawn(func() { s.logErrors(ctx) })

	return eng, nil
}

// routePlans hands each plan event to the Intake Handler of the repository it names.
func (s *stack) routePlans(ctx context.Context) {
	sub, err := s.bus.Subscribe("plan-router", bus.TypePlan)
	if err != nil {
		s.logln("komodo engine: subscribe to plan events:", err)

		return
	}
	defer sub.Close()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub.Events:
			if !ok {
				return
			}

			req, ok := ev.Data.(*intake.PlanRequest)
			if !ok {
				s.logf("komodo engine: plan event carries %T, not a plan request\n", ev.Data)

				continue
			}

			k, err := s.kitFor(req.Root, req.CommonDir, req.Repo)
			if err != nil {
				s.logln("komodo engine: open", req.Root+":", err)

				continue
			}

			if err := k.intake.Handle(ctx, ev.Run, *req); err != nil {
				s.logln("komodo engine: plan:", err)
			}
		}
	}
}

// baseBranch reads commit.base_branch; empty lets the Commit Handler take the remote's default.
func (s *stack) baseBranch() string { return s.str("commit.base_branch", "") }

// thresholds reads the spend and token limits the Resource Manager enforces.
func (s *stack) thresholds() resource.Thresholds {
	return resource.Thresholds{
		USDCeilingPerRun: s.num("limits.usd_ceiling_per_run"),
		USDCeilingPerDay: s.num("limits.usd_ceiling_per_day"),
		GroupTokens:      int64(s.num("limits.group_tokens")),
	}
}

// kitFor returns the kit of a repository, creating it on first use; common and id are read from the
// repository when empty.
func (s *stack) kitFor(root, common, id string) (*repoKit, error) {
	if root == "" {
		return nil, fmt.Errorf("the request names no repository root")
	}

	s.kitMu.Lock()
	defer s.kitMu.Unlock()

	if k := s.kits[root]; k != nil {
		return k, nil
	}

	if common == "" {
		var err error
		if common, err = gitOut(
			"-C",
			root,
			"rev-parse",
			"--path-format=absolute",
			"--git-common-dir",
		); err != nil {
			return nil, fmt.Errorf("find the git folder of %s: %w", root, err)
		}
	}

	if id == "" {
		id = cli.RepoID(root)
	}

	sum := sha256.Sum256([]byte(root))
	k := &repoKit{
		s:        s,
		id:       id,
		root:     root,
		common:   common,
		landings: filepath.Join(s.home, "landing", hex.EncodeToString(sum[:4])),
	}

	tools := map[string][]string{}
	for _, r := range agent.Roles() {
		tools[r.Name] = r.Tools
	}

	cm, err := contextmgr.New(contextmgr.Deps{
		Bus:   s.bus,
		Cache: lazyCache{s.caches, id},
		Home:  s.home,
		Tools: tools,
	})
	if err != nil {
		return nil, fmt.Errorf("open the context manager: %w", err)
	}

	k.cm = cm
	if k.st, err = state.New(filepath.Join(common, "komodo"), "engine"); err != nil {
		return nil, fmt.Errorf("open the group records: %w", err)
	}

	s.git.Adopt(root)
	k.cleanup = &cleanup.Handler{
		Lease:  s.leases,
		Git:    s.git,
		Boxes:  s.containers(),
		Task:   &groupRef{},
		Groups: unfinished{k.st},
		Live:   s.leases.Live,
		Report: reporter{s}, Repo: id, Clone: root, Remote: "origin", Current: version,
		Roots: cleanup.Roots{
			Worktrees: filepath.Join(s.home, "wt", path.Base(id)),
			Landings:  k.landings,
			Versions:  filepath.Join(s.home, "versions"),
		},
	}
	k.commit = &commit.Handler{
		Git: s.git, Forge: commit.NewGH(root), Leases: s.leases, Epics: &commit.EpicGates{},
		Check:   groupChecker{ref: &groupRef{}},
		Report:  commitReporter{s},
		Cleanup: publishedAdapter{h: k.cleanup, landing: k.landingPath},
		Config:  commit.Settings{LandingRoot: k.landings},
	}
	k.intake = intake.New(k.intakeDeps(k.cm))
	s.kits[root] = k
	s.byID[id] = k
	s.spawn(func() { _ = cm.Run(s.ctx) })

	return k, nil
}

// leaseRepos names each clone and repository id found in the lease registry, which survives a
// killed engine.
func (s *stack) leaseRepos() map[string]string {
	out := map[string]string{}

	files, _ := filepath.Glob(filepath.Join(s.home, "leases", "*", "*.json"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}

		var l lease.Lease
		if json.Unmarshal(b, &l) == nil && l.ClonePath != "" {
			if _, err := os.Stat(l.ClonePath); err == nil {
				out[l.ClonePath] = l.Repo
			}
		}
	}

	return out
}

// refFor returns the Task Manager reference of a group, creating an empty one for a group never
// started.
func (s *stack) refFor(group string) *groupRef {
	s.kitMu.Lock()
	defer s.kitMu.Unlock()

	r := s.refs[group]
	if r == nil {
		r = &groupRef{}
		s.refs[group] = r
	}

	return r
}

// logErrors writes every error event to the engine log, so a failure with no open run still leaves
// a trace.
func (s *stack) logErrors(ctx context.Context) {
	sub, err := s.bus.Subscribe("engine-log", bus.TypeError)
	if err != nil {
		return
	}
	defer sub.Close()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub.Events:
			if !ok {
				return
			}

			b, _ := json.Marshal(ev.Data)
			s.logf("komodo engine: error %s %s\n", ev.Subtype, b)
		}
	}
}

// stateOpener opens the State Manager under a repository's Komodo folder.
func stateOpener(common string) (engine.StateStore, error) {
	m, err := state.New(filepath.Join(common, "komodo"), "engine")
	if err != nil {
		return nil, err
	}

	return stateStore{m}, nil
}
