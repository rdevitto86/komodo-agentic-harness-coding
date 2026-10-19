package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// Engine is the one background process per user: it serves the control socket and hosts the Runtime Manager.
type Engine struct {
	d       Deps
	m       *Manager
	clk     Clock
	t       Timing
	sock    string
	printer *ConnPrinter
	q       *queue

	mu    sync.Mutex
	conns map[net.Conn]struct{}
	// the exit rule held; new connections get engine_stopping
	closing bool
	// a connection, a run or a queued entry has existed, so the exit rule applies
	seen bool
	// closed when the exit rule holds
	exit chan struct{}
}

// ErrEngineRunning marks a start while another engine already answers on the socket.
var ErrEngineRunning = errors.New("another engine already runs for this user")

// NewEngine wires an engine around a Runtime Manager; socketPath is the control socket.
func NewEngine(d Deps, socketPath string) *Engine {
	e := &Engine{
		d:       d,
		m:       New(d),
		clk:     clockOf(d.Clock),
		t:       d.Timing.filled(),
		sock:    socketPath,
		printer: &ConnPrinter{},
		conns:   map[net.Conn]struct{}{},
		exit:    make(chan struct{}),
	}
	e.q = &queue{e: e, kick: make(chan struct{}, 1), firing: map[string]bool{}}
	return e
}

// Manager returns the engine's Runtime Manager, which the Resource Manager and the watcher loops call.
func (e *Engine) Manager() *Manager { return e.m }

// Printer returns the engine's fault printer.
func (e *Engine) Printer() *ConnPrinter { return e.printer }

// Serve listens on the socket, opens each repository's cache at its first run, runs the schedule queue and returns when no
// run is live or queued.
func (e *Engine) Serve(ctx context.Context) error {
	ln, unlock, err := e.bind(ctx)
	if err != nil {
		return err
	}

	runCtx, stopRun := context.WithCancel(context.Background())
	defer stopRun()

	mdone := make(chan error, 1)
	e.m.spawn(func() { mdone <- e.m.Run(runCtx) })

	select {
	case <-e.m.Ready():
	case err := <-mdone:
		_ = ln.Close()
		_ = os.Remove(e.sock)
		_ = unlock()
		return fmt.Errorf("start the runtime manager: %w", err)
	}

	_ = unlock()
	if es, _, err := e.q.load(); err == nil && len(es) > 0 {
		e.markSeen()
	}

	e.m.spawn(func() { e.beatLoop(runCtx) })
	e.m.spawn(func() { e.q.loop(runCtx) })
	e.m.spawn(func() { e.watchChanges(runCtx) })
	e.m.spawn(func() { e.accept(runCtx, ln) })

	var serveErr error
	select {
	case <-e.exit:
	case <-ctx.Done():
		e.mu.Lock()
		e.closing = true
		e.mu.Unlock()
		e.m.StopAll(context.Background())
	case err := <-mdone:
		serveErr = err
	}

	_ = ln.Close()
	_ = os.Remove(e.sock)
	_ = os.Remove(e.beatPath())
	e.closeConns()
	stopRun()
	e.m.wait(killGrace)
	e.m.closeAllCaches()
	return serveErr
}

// bind takes the socket: a file nothing answers on is a leftover and is replaced, and a live engine makes this process exit.
func (e *Engine) bind(ctx context.Context) (net.Listener, func() error, error) {
	if err := os.MkdirAll(filepath.Dir(e.sock), 0o700); err != nil {
		return nil, nil, fmt.Errorf("create the state folder: %w", err)
	}

	unlock, err := core.FileLock(ctx, e.sock+".lock", 10*time.Second)
	if err != nil {
		return nil, nil, fmt.Errorf("lock the control socket: %w", err)
	}
	if c, err := Dial(e.sock); err == nil {
		_ = c.Close()
		_ = unlock()
		return nil, nil, ErrEngineRunning
	}
	if err := os.Remove(e.sock); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = unlock()
		return nil, nil, fmt.Errorf("remove the old control socket: %w", err)
	}

	ln, err := net.Listen("unix", e.sock)
	if err != nil {
		_ = unlock()
		return nil, nil, fmt.Errorf("listen on the control socket: %w", err)
	}
	if err := os.Chmod(e.sock, 0o600); err != nil {
		_ = ln.Close()
		_ = unlock()
		return nil, nil, fmt.Errorf("restrict the control socket: %w", err)
	}

	return ln, unlock, nil
}

// accept takes connections until the listener closes; one that arrives after the exit rule held gets engine_stopping.
func (e *Engine) accept(ctx context.Context, ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}

		e.mu.Lock()
		if e.closing {
			e.mu.Unlock()
			_ = (&connWriter{c: c}).send(Reply{
				Status: StatusRefused,
				Code:   CodeEngineStopping,
				Detail: "the engine is stopping; start a new one",
				Final:  true,
			})
			_ = c.Close()
			continue
		}

		e.conns[c] = struct{}{}
		e.mu.Unlock()
		e.m.spawn(func() { e.serveConn(ctx, c) })
	}
}

// markSeen turns the exit rule on.
func (e *Engine) markSeen() {
	e.mu.Lock()
	e.seen = true
	e.mu.Unlock()
}

// watchChanges runs the exit check after every change to a run or a group.
func (e *Engine) watchChanges(ctx context.Context) {
	for {
		ch := e.m.Changes()
		e.mu.Lock()
		if e.m.Live() > 0 {
			e.seen = true
		}
		e.mu.Unlock()
		e.checkExit()

		select {
		case <-ch:
		case <-ctx.Done():
			return
		}
	}
}

// checkExit closes the exit channel when no run is live, no entry is queued and no connection is open.
func (e *Engine) checkExit() {
	if e.m.Live() > 0 || e.q.length() > 0 {
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.closing || !e.seen || len(e.conns) > 0 {
		return
	}

	e.closing = true
	close(e.exit)
}

// closeConns closes every open connection.
func (e *Engine) closeConns() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for c := range e.conns {
		_ = c.Close()
	}
}

// beatLoop rewrites the heartbeat file every EngineBeatEvery from a goroutine of its own.
func (e *Engine) beatLoop(ctx context.Context) {
	for {
		b, _ := json.Marshal(struct {
			PID int       `json:"pid"`
			At  time.Time `json:"at"`
		}{os.Getpid(), e.clk.Now().UTC().Truncate(time.Second)})
		if err := core.WriteFileAtomic(e.beatPath(), b, 0o600); err != nil {
			e.m.reportErr("", "", fmt.Errorf("write the engine heartbeat: %w", err))
		}

		t := make(chan struct{})
		timer := e.clk.AfterFunc(e.t.EngineBeatEvery, func() { close(t) })
		select {
		case <-t:
		case <-ctx.Done():
			timer.Stop()
			return
		}
	}
}

// beatPath returns the heartbeat file, which sits beside the socket.
func (e *Engine) beatPath() string { return filepath.Join(filepath.Dir(e.sock), BeatName) }
