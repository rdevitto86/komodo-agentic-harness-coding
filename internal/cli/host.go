package cli

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/config"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/sandbox"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// LinkProber probes every model link without a model turn; *llm.Client satisfies it.
type LinkProber interface {
	ProbeAll(ctx context.Context) []llm.ProbeResult
}

// RunMover moves live runs onto the binary at bin through the engine.
type RunMover interface {
	// MoveRuns restarts every live run on bin and returns how many it moved.
	MoveRuns(ctx context.Context, bin string) (int, error)
}

// Host holds what install, update and doctor read from the machine; a zero field uses the real machine, or is skipped where none exists.
type Host struct {
	Links      LinkProber                        // the model client; nil leaves the links unprobed
	Sandbox    sandbox.Backend                   // the operating-system sandbox; Seatbelt or Bubblewrap by platform when nil
	Docker     sandbox.Backend                   // the Docker backend; docker on PATH when nil
	Rules      func() ([]agent.Precision, error) // per-rule review precision; nil means no review data
	Runs       RunMover                          // moves live runs for update --restart-runs; nil refuses the flag
	InUse      func() ([]string, error)          // versions that unfinished task groups name; nil means none
	Style      func() (string, error)            // the orchestrator rules text for the Komodo block; nil writes no rules line
	Release    ReleaseConfig                     // where update finds releases and which keys sign them
	LookPath   func(name string) (string, error) // finds a program on PATH; exec.LookPath when nil
	Executable func() (string, error)            // the running binary's path; os.Executable when nil
	FSType     func(dir string) (string, error)  // the file system type under dir; the platform call when nil
	Now        func() time.Time                  // the clock; time.Now when nil
}

// lookPath finds a program on PATH.
func (h Host) lookPath(name string) (string, error) {
	if h.LookPath != nil {
		return h.LookPath(name)
	}
	return exec.LookPath(name)
}

// executable returns the running binary's path.
func (h Host) executable() (string, error) {
	if h.Executable != nil {
		return h.Executable()
	}
	return os.Executable()
}

// now returns the current time.
func (h Host) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// fsType returns the file system type under dir.
func (h Host) fsType(dir string) (string, error) {
	if h.FSType != nil {
		return h.FSType(dir)
	}
	return fsTypeOf(dir)
}

// osSandbox returns the operating-system sandbox backend.
func (h Host) osSandbox() sandbox.Backend {
	if h.Sandbox != nil {
		return h.Sandbox
	}
	if runtime.GOOS == "darwin" {
		return sandbox.NewSeatbelt(sandbox.SeatbeltOptions{})
	}
	return sandbox.NewBubblewrap(sandbox.BubblewrapOptions{})
}

// docker returns the Docker backend.
func (h Host) docker() sandbox.Backend {
	if h.Docker != nil {
		return h.Docker
	}
	return sandbox.NewDocker(sandbox.DockerOptions{})
}

// komodoHome returns the Komodo home folder: the one in Deps, else KOMODO_HOME, else ~/.komodo.
func (c *CLI) komodoHome() (string, error) {
	if c.deps.Home != "" {
		return c.deps.Home, nil
	}
	return config.DefaultHome()
}

// machine returns the State Manager over the machine-wide records, opening it with the cli writer when Deps holds none.
func (c *CLI) machine() (*state.Machine, error) {
	if c.deps.State != nil {
		return c.deps.State, nil
	}
	home, err := c.komodoHome()
	if err != nil {
		return nil, err
	}
	return state.NewMachine(home, "cli")
}
