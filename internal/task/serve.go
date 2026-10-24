package task

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// BeatEvery is how often the command loop sends a beat message to the Runtime Manager.
const BeatEvery = 5 * time.Second

// Channels connect a Task Manager goroutine to the Runtime Manager that started it.
type Channels struct {
	Commands <-chan Command // from the Runtime Manager
	Replies  chan<- Message // to the Runtime Manager; closed when Serve returns
}

// CommandKind names an instruction on the command channel.
type CommandKind string

// Command is one instruction the Runtime Manager sends to one Task Manager.
type Command struct {
	Kind   CommandKind `json:"kind"`
	Re     string      `json:"re,omitempty"`     // the Message.ID this command answers
	Answer *Answer     `json:"answer,omitempty"` // continue: the one reply to an Escalation
	Grant  *TimerGrant `json:"grant,omitempty"`  // continue: the reply to a TimerRequest
	Wake   *Wake       `json:"wake,omitempty"`   // continue: resume a paused group
	Link   string      `json:"link,omitempty"`   // pause: the link whose window ran out
	Until  time.Time   `json:"until,omitzero"`   // pause: when the window resets; zero when unknown
	Cause  string      `json:"cause,omitempty"`  // stop and pause: why
}

// Wake says why the Runtime Manager resumed a paused group.
type Wake struct {
	Reason string // timer or run-continue
	At     time.Time
}

// MessageKind names an item on the reply channel.
type MessageKind string

// Message is one item the Task Manager sends to the Runtime Manager.
type Message struct {
	ID         string        `json:"id"` // unique; a Command's Re names it
	Kind       MessageKind   `json:"kind"`
	Escalation *Escalation   `json:"escalation,omitempty"`
	Timer      *TimerRequest `json:"timer,omitempty"`
	State      *StateReport  `json:"state,omitempty"`
	Ended      *Ended        `json:"ended,omitempty"`
}

// Ended says how a call that ran the pipeline ended.
type Ended struct {
	Result string `json:"result"`          // closed, held, paused, stopped or failed
	Error  string `json:"error,omitempty"` // the error text when Result is failed
}

// chanRuntime is the Runtime over the reply channel: each call is a message, and a timer request waits for the grant that
// names it.
type chanRuntime struct {
	ctx   context.Context
	out   chan<- Message
	tag   string // makes the identifiers differ from those of an earlier start of the same group
	seq   atomic.Int64
	beats atomic.Int64
	mu    sync.Mutex
	waits map[string]chan TimerGrant
	sendM sync.RWMutex // held while a send is in flight; shut takes it to close the channel
	shut  bool
}

// newChanRuntime returns the Runtime that sends its messages on out.
func newChanRuntime(ctx context.Context, out chan<- Message) *chanRuntime {
	return &chanRuntime{ctx: ctx, out: out, tag: newTag(), waits: map[string]chan TimerGrant{}}
}

// newTag returns 8 random hex characters.
func newTag() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Escalate sends the escalation under its own identifier.
func (c *chanRuntime) Escalate(_ context.Context, e Escalation) error {
	return c.send(Message{ID: e.ID, Kind: MsgEscalate, Escalation: &e})
}

// ReportState sends one state report.
func (c *chanRuntime) ReportState(_ context.Context, s StateReport) error {
	return c.send(Message{ID: fmt.Sprintf("state-%s-%d", c.tag, c.seq.Add(1)), Kind: MsgState, State: &s})
}

// ExtendTimer sends a timer message and blocks until the continue command that names it arrives.
func (c *chanRuntime) ExtendTimer(ctx context.Context, r TimerRequest) (TimerGrant, error) {
	id := fmt.Sprintf("tmr-%s-%d", c.tag, c.seq.Add(1))
	ch := make(chan TimerGrant, 1)

	c.mu.Lock()
	c.waits[id] = ch

	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.waits, id)
		c.mu.Unlock()
	}()

	if err := c.send(Message{ID: id, Kind: MsgTimer, Timer: &r}); err != nil {
		return TimerGrant{}, err
	}

	select {
	case g := <-ch:
		return g, nil
	case <-ctx.Done():
		return TimerGrant{}, ctx.Err()
	case <-c.ctx.Done():
		return TimerGrant{}, c.ctx.Err()
	}
}

// send puts one message on the reply channel and waits while it is full.
func (c *chanRuntime) send(m Message) error {
	c.sendM.RLock()
	defer c.sendM.RUnlock()

	if c.shut {
		return context.Canceled
	}

	select {
	case c.out <- m:
		return nil
	case <-c.ctx.Done():
		return c.ctx.Err()
	}
}

// beat puts a beat on the reply channel without waiting; a full channel drops it, since unread messages show the loop lives.
func (c *chanRuntime) beat() {
	c.sendM.RLock()
	defer c.sendM.RUnlock()

	if c.shut {
		return
	}

	select {
	case c.out <- Message{ID: fmt.Sprintf("beat-%s-%d", c.tag, c.beats.Add(1)), Kind: MsgBeat}:
	default:
	}
}

// close ends the reply channel once no send is in flight; later sends fail.
func (c *chanRuntime) close() {
	c.sendM.Lock()
	defer c.sendM.Unlock()
	c.shut = true
	close(c.out)
}

// grant hands a timer grant to the call that waits under the identifier; false when none waits.
func (c *chanRuntime) grant(re string, g TimerGrant) bool {
	c.mu.Lock()
	ch := c.waits[re]
	c.mu.Unlock()
	if ch == nil {
		return false
	}

	select {
	case ch <- g:
	default:
	}

	return true
}

// serveLoop is the state of one Serve call.
type serveLoop struct {
	m     *Manager
	ctx   context.Context
	cr    *chanRuntime
	wg    sync.WaitGroup
	calls chan error
	busy  atomic.Bool
}

// The command kinds.
const (
	CmdStart    CommandKind = "start"    // run the pipeline
	CmdPause    CommandKind = "pause"    // hold the group after its current step
	CmdContinue CommandKind = "continue" // an answer, a timer grant or a wake-up
	CmdStop     CommandKind = "stop"     // cancel live sessions and end
	CmdNudge    CommandKind = "nudge"    // add the token-cap nudge to each live session
	CmdRecheck  CommandKind = "recheck"  // run the free checks on the epic tip in a fresh container
)

// The message kinds.
const (
	MsgEscalate MessageKind = "escalate" // a blocker or a request
	MsgTimer    MessageKind = "timer"    // a request for more wall time
	MsgState    MessageKind = "state"    // progress and the facts since the last report
	MsgEnded    MessageKind = "ended"    // Run, Resume or Continue returned
	MsgBeat     MessageKind = "beat"     // the command loop is alive
)

// dispatch acts on one command; it returns true when the loop ends.
func (l *serveLoop) dispatch(cmd Command) bool {
	m := l.m
	drop := func(why string) {
		m.fact("command.dropped", map[string]string{"kind": string(cmd.Kind), "re": cmd.Re, "why": why})
	}

	switch cmd.Kind {
	case CmdStart:
		if l.busy.Load() {
			drop("a call is running")
			return false
		}
		l.start(m.Run)
	case CmdPause:
		if err := m.Pause(l.ctx, cmd.Link, cmd.Until); err != nil {
			drop(err.Error())
		}
	case CmdStop:
		_ = m.Stop(l.ctx, cmd.Cause)
		if l.busy.Load() {
			l.ended(<-l.calls)
			return true
		}
		e := Ended{Result: "stopped"}
		_ = l.cr.send(Message{ID: m.nextID("end"), Kind: MsgEnded, Ended: &e})
		return true
	case CmdNudge:
		m.Nudge()
	case CmdRecheck:
		l.wg.Add(1)
		go func() {
			defer guardian.Recover(taskSource, nil)
			defer l.wg.Done()
			m.Recheck(l.ctx)
		}()
	case CmdContinue:
		l.deliver(cmd, drop)
	default:
		drop("unknown command")
	}

	return false
}

// start runs one pipeline call on its own goroutine, so the loop keeps reading commands.
func (l *serveLoop) start(f func(context.Context) error) {
	l.busy.Store(true)
	l.wg.Add(1)

	go func() {
		defer guardian.Recover(taskSource, nil)

		finished := false

		defer l.wg.Done()
		defer func() {
			if !finished {
				l.calls <- errors.New("the pipeline call crashed")
			}
		}()

		err := f(l.ctx)
		finished = true

		l.calls <- err
	}()
}

// ended sends the end of a call; it returns true when the group is done with the loop.
func (l *serveLoop) ended(err error) bool {
	l.busy.Store(false)
	e := l.m.endedMessage(err)
	_ = l.cr.send(Message{ID: l.m.nextID("end"), Kind: MsgEnded, Ended: &e})
	return e.Result == "closed" || e.Result == "stopped" || e.Result == "failed"
}

// deliver routes a continue command by what it carries: a timer grant, an answer or a wake-up.
func (l *serveLoop) deliver(cmd Command, drop func(string)) {
	m := l.m

	switch {
	case cmd.Grant != nil:
		if !l.cr.grant(cmd.Re, *cmd.Grant) {
			drop("no timer request waits")
		}
	case cmd.Answer != nil:
		a := *cmd.Answer
		if l.busy.Load() || !m.holds(a.Escalation) {
			drop(ErrNoEscalation.Error())
			return
		}
		l.start(func(c context.Context) error { return m.Continue(c, a) })
	case cmd.Wake != nil:
		w := *cmd.Wake
		if l.busy.Load() || !m.isPaused() {
			drop(ErrNotPaused.Error())
			return
		}
		l.start(func(c context.Context) error { return m.Resume(c, w) })
	default:
		drop("a continue with nothing to deliver")
	}
}

// Serve runs the command loop of the Task Manager goroutine until the group is closed or stopped.
func (m *Manager) Serve(ctx context.Context, ch Channels) error {
	sctx, cancel := context.WithCancel(ctx)
	l := &serveLoop{m: m, ctx: sctx, calls: make(chan error, 1),
		cr: newChanRuntime(sctx, ch.Replies)}
	m.mu.Lock()
	if m.rt == nil {
		m.rt = l.cr
	}

	m.mu.Unlock()
	defer func() {
		// Queued facts go up first; then the work still running is cancelled, awaited, and the reply channel closes.
		if m.hasFacts() {
			_ = m.report(sctx)
		}
		cancel()
		l.wg.Wait()
		l.cr.close()
	}()

	// The loop beats from its own select, so a pipeline call in a long session still beats and a hung loop does not.
	ticker := time.NewTicker(cmpOrDur(m.c.BeatEvery, BeatEvery))
	defer ticker.Stop()

	l.cr.beat()

	for {
		select {
		case <-ctx.Done():
			m.stopCancel(ErrStopped)
			return ctx.Err()
		case <-ticker.C:
			l.cr.beat()
		case err := <-l.calls:
			if l.ended(err) {
				return nil
			}
		case cmd, ok := <-ch.Commands:
			if !ok {
				m.stopCancel(ErrStopped)
				return nil
			}
			if l.dispatch(cmd) {
				return nil
			}
		}
	}
}

// Recheck runs the mechanical checks in a sandbox instance made now, after the landing, so its mounts show the epic tip, and
// destroys the instance when the checks end. It hands the verdict to OnRecheck.
func (m *Manager) Recheck(ctx context.Context) {
	if m.c.OnRecheck == nil {
		return
	}

	inst, err := m.newInstance(ctx, "qa", "recheck")
	if err != nil {
		m.c.OnRecheck(false, err.Error())
		return
	}

	defer m.destroyInstance(ctx, inst.ID)

	// The epic tip holds the other groups' landed work, so their files are in scope for the recheck.
	fails, err := m.runChecks(ctx, m.runnerOn("recheck", inst.ID), m.spec.OtherFiles)
	switch {
	case err != nil:
		m.c.OnRecheck(false, err.Error())
	case len(fails) > 0:
		m.c.OnRecheck(false, strings.Join(fails, "\n"))
	default:
		m.c.OnRecheck(true, "")
	}
}

// runtime returns the Runtime the Manager reports to.
func (m *Manager) runtime() Runtime {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rt
}

// hasFacts reports whether facts wait for the next report.
func (m *Manager) hasFacts() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.facts) > 0
}

// holds reports whether the escalation with this identifier is the open one.
func (m *Manager) holds(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.open != nil && m.open.ID == id
}

// isPaused reports whether the group waits on a pause.
func (m *Manager) isPaused() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.prog.Pause != nil
}

// endedMessage maps how a call ended to the result the Runtime Manager reads.
func (m *Manager) endedMessage(err error) Ended {
	var fe *failedErr
	if m.closing.Load() && (err == nil || errors.Is(err, ErrStopped)) {
		<-m.closed
		return Ended{Result: "closed"}
	}

	switch {
	case errors.Is(err, ErrStopped), err == nil && m.isStopped():
		return Ended{Result: "stopped"}
	case err == nil:
		return Ended{Result: "closed"}
	case errors.Is(err, ErrHeld):
		return Ended{Result: "held"}
	case errors.Is(err, ErrGroupPaused):
		return Ended{Result: "paused"}
	case errors.As(err, &fe):
		return Ended{Result: "failed", Error: fe.Error()}
	}

	return Ended{Result: "failed", Error: err.Error()}
}
