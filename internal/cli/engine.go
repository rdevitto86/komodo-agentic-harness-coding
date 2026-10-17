package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/fault"
)

// SocketEngine is the Engine that talks to engine.sock; only this file opens the control socket.
type SocketEngine struct {
	Home      string                          // the Komodo home folder holding state/engine.sock
	Exe       string                          // the binary that starts the engine; the running binary when empty
	Args      []string                        // the arguments that start the engine
	Start     func(ctx context.Context) error // starts a detached engine; spawns Exe with Args when nil
	Kill      func(pid int) error             // stops an engine's process tree; signals its process group when nil
	Redial    time.Duration                   // pause between dials of a starting engine
	StartWait time.Duration                   // how long a started engine has to answer
	HungReply time.Duration                   // silence before the beat file is checked
	HungBeat  time.Duration                   // beat age that marks the engine hung
	Now       func() time.Time
}

// NewSocketEngine returns a client for the engine under home with the design's timings.
func NewSocketEngine(home string) *SocketEngine {
	return &SocketEngine{
		Home:      home,
		Args:      []string{"internal", "engine"},
		Redial:    50 * time.Millisecond,
		StartWait: 5 * time.Second,
		HungReply: 10 * time.Second,
		HungBeat:  30 * time.Second,
		Now:       time.Now,
	}
}

// socketPath returns the control socket path.
func (e *SocketEngine) socketPath() string { return filepath.Join(e.Home, "state", "engine.sock") }

// beatPath returns the engine heartbeat file path.
func (e *SocketEngine) beatPath() string { return filepath.Join(e.Home, "state", "engine.beat") }

// Ensure connects to the engine, starting one first when none answers.
func (e *SocketEngine) Ensure(ctx context.Context) error {
	conn, err := e.connect(ctx)
	if err != nil {
		return err
	}
	return conn.Close()
}

// connect dials the socket, starting the engine and redialling when the first dial fails.
func (e *SocketEngine) connect(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	if n := len(e.socketPath()); n > maxSocketPath {
		return nil, fmt.Errorf(
			"the socket path %s is %d bytes, over the %d a unix socket allows; set KOMODO_HOME to a shorter folder",
			e.socketPath(),
			n,
			maxSocketPath,
		)
	}

	conn, err := d.DialContext(ctx, "unix", e.socketPath())
	if err == nil {
		return conn, nil
	}
	if !engineAbsent(err) {
		return nil, fmt.Errorf("dial %s: %w", e.socketPath(), err)
	}

	start := e.Start
	if start == nil {
		start = e.spawn
	}
	if err := start(ctx); err != nil {
		return nil, fmt.Errorf("start the engine: %w", err)
	}

	deadline := time.Now().Add(e.StartWait)

	for {
		conn, err := d.DialContext(ctx, "unix", e.socketPath())
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !engineAbsent(err) {
			return nil, fmt.Errorf("dial %s: %w", e.socketPath(), err)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf(
				"no engine answered on %s within %s: %w", e.socketPath(), e.StartWait, err,
			)
		}
		time.Sleep(e.Redial)
	}
}

// maxSocketPath is the longest unix socket path every supported system accepts.
const maxSocketPath = 103

// engineAbsent reports whether a failed dial means no engine listens: the socket file is missing or refuses.
func engineAbsent(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}

// spawn starts the engine as a detached process in its own process group.
func (e *SocketEngine) spawn(context.Context) error {
	exe := e.Exe
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return fmt.Errorf("find the komodo binary: %w", err)
		}
	}

	cmd := exec.Command(exe, e.Args...)
	cmd.Env = append(os.Environ(), "KOMODO_HOME="+e.Home)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// Send writes one request and returns the replies until the one that ends it.
func (e *SocketEngine) Send(ctx context.Context, r engine.Request) (<-chan engine.Reply, error) {
	conn, err := e.connect(ctx)
	if err != nil {
		return nil, err
	}

	line, err := json.Marshal(r)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("encode request: %w", err)
	}

	line = append(line, '\n')
	if _, err := conn.Write(line); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("write request: %w", err)
	}

	ch := make(chan engine.Reply, 16)
	go func() {
		defer fault.New(nil, nil).Recover(fault.Source{Component: "cli"}, nil)
		e.pump(ctx, conn, r.ID, line, ch)
	}()

	return ch, nil
}

// pump forwards reply frames until the final one; it resends the request once to a new engine
// when the first answers engine_stopping or hangs. A broken connection ends with an error frame.
func (e *SocketEngine) pump(ctx context.Context, conn net.Conn, id string, line []byte, ch chan<- engine.Reply) {
	defer close(ch)

	retried, first := false, true
	rd := bufio.NewReader(conn)

	defer func() { _ = conn.Close() }()

	// TODO - needs small comment on functioality
	fail := func(detail string) {
		ch <- engine.Reply{
			ID:     id,
			Status: engine.StatusError,
			Code:   "internal",
			Detail: detail,
			Final:  true,
		}
	}

	// TODO - needs small comment on functioality
	resend := func() bool {
		_ = conn.Close()
		nc, err := e.connect(ctx)
		if err != nil {
			fail(err.Error())
			return false
		}
		conn, rd, retried = nc, bufio.NewReader(nc), true
		if _, err := conn.Write(line); err != nil {
			fail("write request: " + err.Error())
			return false
		}
		return true
	}

	// TODO - needs small comment on functioality
	for {
		_ = conn.SetReadDeadline(e.now().Add(e.HungReply))
		b, err := rd.ReadBytes('\n')
		if errors.Is(err, os.ErrDeadlineExceeded) {
			if !e.hung() {
				continue
			}
			if retried {
				fail("the engine is hung and a fresh engine did not answer")
				return
			}
			e.killEngine()
			if !resend() {
				return
			}
			continue
		}
		if err != nil {
			fail("the connection to the engine broke: " + err.Error())
			return
		}

		var rep engine.Reply
		if err := json.Unmarshal(b, &rep); err != nil {
			fail("read an engine reply: " + err.Error())
			return
		}
		if first && !retried && rep.Status == engine.StatusRefused && rep.Code == engine.CodeEngineStopping {
			time.Sleep(e.Redial)
			if !resend() {
				return
			}
			continue
		}

		first = false

		select {
		case ch <- rep:
		case <-ctx.Done():
			return
		}
		if rep.Final {
			return
		}
	}
}

// now returns the current time from the injectable clock.
func (e *SocketEngine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// beat is the content of engine.beat.
type beat struct {
	PID int       `json:"pid"`
	At  time.Time `json:"at"`
}

// readBeat reads the engine heartbeat file.
func (e *SocketEngine) readBeat() (beat, bool) {
	b, err := os.ReadFile(e.beatPath())
	if err != nil {
		return beat{}, false
	}

	var h beat
	if json.Unmarshal(b, &h) != nil {
		return beat{}, false
	}
	return h, true
}

// hung reports whether the beat file is older than HungBeat; a missing beat file is not proof of a hang.
func (e *SocketEngine) hung() bool {
	h, ok := e.readBeat()
	return ok && e.now().Sub(h.At) > e.HungBeat
}

// killEngine stops the engine's whole process tree: it leads its own process group.
func (e *SocketEngine) killEngine() {
	h, ok := e.readBeat()
	if !ok || h.PID <= 1 {
		return
	}

	kill := e.Kill
	if kill == nil {
		kill = func(pid int) error { return syscall.Kill(-pid, syscall.SIGKILL) }
	}
	_ = kill(h.PID)
}
