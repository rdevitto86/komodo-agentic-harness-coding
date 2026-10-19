package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cache"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/config"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/contextmgr"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/fault"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/intake"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/lease"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/sandbox"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

// stack holds the parts the engine process shares: the bus, the settings, the models and the stores.
type stack struct {
	home    string
	cfg     *config.Manager
	bus     bus.Bus
	reader  bus.Reader
	machine *state.Machine
	leases  *lease.Manager
	llm     *llm.Client
	catalog *llm.Catalog
	faults  *fault.Handler
	boxes   *sandbox.Manager
	git     *core.Git
	ctx     context.Context
	mgr     *engine.Manager
	caches  *cacheStore
	kitMu   sync.Mutex
	kits    map[string]*repoKit
	byID    map[string]*repoKit
	refs    map[string]*groupRef
	stderr  io.Writer
	closers []func()
}

// registerAllEvents binds every package's event types once per process, before the first publish.
var registerAllEvents sync.Once

// version is the binary version a run is pinned to; the release build sets it.
var version = "1.0.0-beta.6"

// newStack opens the stores and the model links under the Komodo home folder.
func newStack(home string, stderr io.Writer) (*stack, error) {
	registerAllEvents.Do(registerEvents)

	cat, err := llm.LoadCatalog()
	if err != nil {
		return nil, fmt.Errorf("load the model catalog: %w", err)
	}

	cfg, err := config.New(home, config.Known{GuardRows: task.KnownRows(), Tools: task.KnownTools()}, llm.NewModelCheck(cat))
	if err != nil {
		return nil, fmt.Errorf("open settings: %w", err)
	}

	s := &stack{home: home, cfg: cfg, catalog: cat, stderr: stderr}
	s.bus = bus.New(bus.Options{EventsDir: filepath.Join(home, "events")})

	s.reader = bus.NewReader(filepath.Join(home, "events"))
	if s.machine, err = state.NewMachine(home, "engine"); err != nil {
		return nil, fmt.Errorf("open the machine records: %w", err)
	}

	lockS, _ := cfg.GetConfigItem("lease.lock_timeout_s")
	if s.leases, err = lease.New(home, lockS.Value, nil, nil); err != nil {
		return nil, fmt.Errorf("open the lease manager: %w", err)
	}

	s.faults = fault.New(s.bus, &engine.ConnPrinter{})
	s.llm = buildLinks(cfg, cat, s.faults)
	s.boxes = sandbox.NewManager(buildBackends(home), nil)
	s.git = core.NewGit()
	s.caches = &cacheStore{home: home, version: version, open: map[string]*cache.Cache{}}
	s.kits, s.byID, s.refs = map[string]*repoKit{}, map[string]*repoKit{}, map[string]*groupRef{}
	s.closers = append(s.closers, func() { _ = s.caches.Close(context.Background()) })
	return s, nil
}

// close runs every registered closer.
func (s *stack) close() {
	for i := len(s.closers) - 1; i >= 0; i-- {
		s.closers[i]()
	}
}

// spawn starts a goroutine whose panic the fault handler reports.
func (s *stack) spawn(fn func()) { s.faults.Go(fault.Source{Component: "engine"}, fn, nil) }

// logf writes one line to the engine log; a failed write is dropped.
func (s *stack) logf(format string, a ...any) { _, _ = fmt.Fprintf(s.stderr, format, a...) }

// logln writes one line to the engine log; a failed write is dropped.
func (s *stack) logln(a ...any) { _, _ = fmt.Fprintln(s.stderr, a...) }

// registerEvents binds every package's event types.
func registerEvents() {
	fault.RegisterEvents()
	intake.RegisterEvents()
	contextmgr.RegisterEvents()
	engine.RegisterEvents()
	registerHandlerEvents()
}

// buildLinks builds the model client from the settings: the paid interface when its key is set and the subscription program.
// Local endpoints are not built, because how their model names are
// written is an open decision.
func buildLinks(cfg *config.Manager, cat *llm.Catalog, faults llm.Recoverer) *llm.Client {
	links := map[llm.Link]llm.Provider{}

	if it, err := cfg.GetConfigItem("providers.keys.anthropic"); err == nil {
		if name, ok := strings.CutPrefix(fmt.Sprint(it.Value), "env:"); ok && os.Getenv(name) != "" {
			var base string
			if u, err := cfg.GetConfigItem("providers.anthropic.url"); err == nil {
				base, _ = u.Value.(string)
			}

			a, err := llm.NewAnthropic(llm.AnthropicOptions{
				BaseURL: base,
				Key:     os.Getenv(name),
				Catalog: cat,
				Faults:  faults,
			})
			if err == nil {
				links[llm.AnthropicAPI] = a
			}
		}
	}

	links[llm.ClaudeCLI] = llm.NewClaude(llm.ClaudeOptions{Catalog: cat, Faults: faults})
	links[llm.OpenAISubscription] = llm.NewOpenAISubscription(llm.OpenAISubscriptionOptions{Faults: faults})

	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		a, err := llm.NewOpenAIAPI(llm.OpenAIAPIOptions{BaseURL: os.Getenv("OPENAI_BASE_URL"), Key: key, Faults: faults})
		if err == nil {
			links[llm.OpenAIAPI] = a
		}
	}

	return llm.New(links)
}

// buildBackends picks the operating-system sandbox of each role by platform.
func buildBackends(home string) map[string]sandbox.Backend {
	var b sandbox.Backend
	if _, err := exec.LookPath("sandbox-exec"); err == nil {
		b = sandbox.NewSeatbelt(sandbox.SeatbeltOptions{Home: home})
	} else {
		b = sandbox.NewBubblewrap(sandbox.BubblewrapOptions{Home: home})
	}
	return map[string]sandbox.Backend{"builder": b, "tester": b}
}

// gitOut runs the host git in the working directory and returns its trimmed output.
func gitOut(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	return strings.TrimSpace(string(out)), err
}

// runEngine builds the harness and serves the control socket in the foreground until the exit rule holds or ctx ends.
func runEngine(ctx context.Context, home string, stderr io.Writer) error {
	s, err := newStack(home, stderr)
	if err != nil {
		return err
	}
	defer s.close()

	s.dumpOnSignal()

	eng, err := s.buildEngine(ctx)
	if err != nil {
		return err
	}

	err = eng.Serve(ctx)
	if errors.Is(err, engine.ErrEngineRunning) {
		return nil
	}

	return err
}
