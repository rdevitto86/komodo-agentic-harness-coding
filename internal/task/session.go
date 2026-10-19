package task

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// Reason is how a session ended; the State Manager owns the list.
type Reason = state.Reason

// SessionLimits are the resolved clocks of one session.
type SessionLimits struct {
	Wall  time.Duration
	Idle  time.Duration
	Grace time.Duration // grace after the nudge; IdleGrace when zero
}

// Clocks is everything one timer tick reads about a session.
type Clocks struct {
	Started      time.Time     // when the session was spawned
	PausedFor    time.Duration // time this session spent paused, excluded from wall
	LastLine     time.Time     // last stream line or tool call
	RetryUntil   time.Time     // end of a retry delay announced by a retry event
	ToolsRunning int           // tool calls in flight
	Warned       bool          // the time-warning line was sent
	NudgedAt     time.Time     // zero until the nudge was sent
}

// Tick decides what one timer tick does; it reads the clocks and touches nothing else.
func (c *Clocks) Tick(now time.Time, lim SessionLimits) TickAction {
	if lim.Wall > 0 {
		used := now.Sub(c.Started) - c.PausedFor
		if used >= lim.Wall {
			return TickStopTimeout
		}
		if !c.Warned && used*100 >= lim.Wall*WarnPercent {
			return TickWarn
		}
	}
	if lim.Idle <= 0 || c.ToolsRunning > 0 || now.Before(c.RetryUntil) {
		return TickNone
	}

	silent := now.Sub(laterOf(c.LastLine, c.RetryUntil))
	switch {
	case c.NudgedAt.IsZero():
		if silent >= lim.Idle {
			return TickNudge
		}
	case c.LastLine.After(c.NudgedAt):
		if silent >= lim.Idle {
			return TickStopIdle
		}
	case now.Sub(c.NudgedAt) >= cmpOrDur(lim.Grace, IdleGrace):
		return TickStopIdle
	}

	return TickNone
}

// TickAction is what one timer tick decides.
type TickAction int

// Hooks are the parts of a supervised session that live outside the stream; every field is optional.
type Hooks struct {
	Now func() time.Time
	// timer period; TickEvery when zero
	Every time.Duration
	// wait from the interrupt to SIGTERM; TermAfter when zero
	TermAfter time.Duration
	// wait from SIGTERM to the whole-tree kill; KillAfter when zero
	KillAfter time.Duration
	// asks the Runtime Manager for more wall time
	Extend func(ctx context.Context, used, limit time.Duration) (time.Duration, error)
	// receives the supervisor's facts
	Fact func(subtype string, data map[string]any)
	// receives each raw stream line
	Raw func(line []byte)
	// tool calls in flight and the last tool activity
	Tools func() (running int, last time.Time)
	// a reason to stop the session at once, such as the refusal limit; empty to go on
	Halt func() Reason
	// lines to send to the live session, such as the token-cap nudge
	Inject <-chan string
}

// next is one stream read.
type next struct {
	ev  llm.StreamEvent
	err error
}

// superviseState is the loop's mutable part: the reason a stop was started for and how far up the ladder it is.
type superviseState struct {
	stop   Reason
	rung   int
	ladder *time.Timer
}

// Timer and ladder constants of one session.
const (
	TickEvery   = 15 * time.Second // one timer per session checks wall, idle and the warning
	WarnPercent = 80               // share of the wall limit that triggers the time warning
	IdleGrace   = 5 * time.Minute  // time a nudged session has before the stop ladder
	TermAfter   = 30 * time.Second // from SIGINT to SIGTERM
	KillAfter   = 60 * time.Second // from SIGTERM to the whole-tree kill
)

// The lines injected into a live session.
const (
	capNudgeBuilder = "The group is at 90% of its token cap; wrap up your current task and save it"
)

// The tick actions.
const (
	TickNone        TickAction = iota
	TickWarn                   // ask for more time, then inject the time-warning line
	TickNudge                  // inject the nudge and start the grace
	TickStopTimeout            // climb the stop ladder, exit reason timeout
	TickStopIdle               // climb the stop ladder, exit reason idle
)

// Supervise reads one stream to its Result, injecting lines and stopping the session as the clocks decide.
func Supervise(ctx context.Context, s llm.Stream, lim SessionLimits, c *Clocks) (llm.Result, Reason, error) {
	return SuperviseWith(ctx, s, lim, c, Hooks{})
}

// SuperviseWith is Supervise with hooks for the Runtime Manager's calls, the facts and the raw stream.
func SuperviseWith(ctx context.Context, s llm.Stream, lim SessionLimits, c *Clocks, h Hooks) (llm.Result, Reason, error) {
	// killGrace is how long the supervisor waits for a stream to end after the whole-tree kill.
	const killGrace = 5 * time.Second

	const (
		nudgeLine      = "Report status or stop now; the harness will ask for your result"
		warnLineFormat = "Finish the current task and stop; %d minutes remain"
	)

	now := h.Now
	if now == nil {
		now = time.Now
	}

	every, term, kill := cmpOrDur(h.Every, TickEvery), cmpOrDur(h.TermAfter, TermAfter), cmpOrDur(h.KillAfter, KillAfter)
	fact := func(sub string, d map[string]any) {
		if h.Fact != nil {
			h.Fact(sub, d)
		}
	}

	rctx, rcancel := context.WithCancel(context.WithoutCancel(ctx))
	defer rcancel()

	reads := make(chan next)

	go func() {
		defer guardian.Recover(taskSource, nil)

		for {
			ev, err := s.Next(rctx)
			select {
			case reads <- next{ev, err}:
			case <-rctx.Done():
				return
			}
			if err != nil || ev.Kind == llm.KindResult {
				return
			}
		}
	}()

	ticker := time.NewTicker(every)
	defer ticker.Stop()

	grants := make(chan time.Duration, 1)
	st := &superviseState{ladder: time.NewTimer(time.Hour)}
	st.ladder.Stop()
	defer st.ladder.Stop()

	climb := func(why Reason) {
		if st.stop == "" {
			st.stop = why
		}

		switch st.rung {
		case 0:
			_ = s.Stop(llm.Interrupt)
			st.ladder.Reset(term)
		case 1:
			_ = s.Stop(llm.Terminate)
			st.ladder.Reset(kill)
		case 2:
			_ = s.Stop(llm.Kill)
			st.ladder.Reset(killGrace)
		}

		st.rung++
	}

	var overage bool

	for {
		select {
		case n := <-reads:
			if n.err != nil {
				return finish(st, llm.Result{}, n.err)
			}

			c.LastLine = now()
			if h.Raw != nil && len(n.ev.Raw) > 0 {
				h.Raw(n.ev.Raw)
			}

			switch n.ev.Kind {
			case llm.KindRetry:
				if n.ev.Retry != nil {
					delay := time.Duration(n.ev.Retry.RetryDelayMS) * time.Millisecond
					c.RetryUntil = now().Add(delay)
				}
			case llm.KindRateLimit:
				if n.ev.RateLimit != nil && n.ev.RateLimit.IsUsingOverage && !overage {
					overage = true
					fact("limit.warn", map[string]any{"kind": "overage"})
				}
			case llm.KindResult:
				if n.ev.Result == nil {
					return finish(st, llm.Result{}, io.ErrUnexpectedEOF)
				}
				return finish(st, *n.ev.Result, nil)
			}

			if halted(h, st) {
				climb(h.Halt())
			}
		case <-ticker.C:
			if h.Tools != nil {
				var last time.Time
				c.ToolsRunning, last = h.Tools()
				c.LastLine = laterOf(c.LastLine, last)
			}
			if st.stop != "" {
				continue
			}
			if halted(h, st) {
				climb(h.Halt())
				continue
			}

			switch c.Tick(now(), lim) {
			case TickWarn:
				c.Warned = true
				used := now().Sub(c.Started) - c.PausedFor

				go func(ctx context.Context) {
					defer guardian.Recover(taskSource, nil)

					var extra time.Duration
					if h.Extend != nil {
						extra, _ = h.Extend(ctx, used, lim.Wall)
					}

					select {
					case grants <- extra:
					case <-ctx.Done():
					}
				}(rctx)
			case TickNudge:
				c.NudgedAt = now()
				fact("session.idle", map[string]any{"idle_s": int64(lim.Idle / time.Second), "nudged": true})
				_ = s.Inject(nudgeLine)
			case TickStopTimeout:
				climb(state.ReasonTimeout)
			case TickStopIdle:
				climb(state.ReasonIdle)
			}
		case extra := <-grants:
			if extra > 0 {
				lim.Wall += extra
			}
			left := lim.Wall - (now().Sub(c.Started) - c.PausedFor)
			mins := int((left + time.Minute - 1) / time.Minute)
			used := now().Sub(c.Started) - c.PausedFor
			fact(
				"session.timewarn",
				map[string]any{"used_s": int64(used / time.Second), "wall_s": int64(lim.Wall / time.Second)},
			)
			_ = s.Inject(fmt.Sprintf(warnLineFormat, mins))
		case line := <-h.Inject:
			_ = s.Inject(line)
		case <-st.ladder.C:
			if st.rung >= 3 {
				return finish(st, llm.Result{}, errors.New("the session did not stop after the whole-tree kill"))
			}
			climb(st.stop)
		case <-ctx.Done():
			if st.stop == "" {
				climb(state.ReasonCancelled)
			}
			ctx = context.WithoutCancel(ctx)
		}
	}
}

// laterOf returns the later of two times.
func laterOf(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// halted reports whether the hooks ask for a stop the loop has not started yet.
func halted(h Hooks, st *superviseState) bool {
	return h.Halt != nil && st.stop == "" && h.Halt() != ""
}

// finish turns the end of the stream into the supervisor's answer.
func finish(st *superviseState, res llm.Result, err error) (llm.Result, Reason, error) {
	if err == nil {
		if st.stop != "" {
			return res, st.stop, nil
		}
		return res, state.ReasonDone, nil
	}

	var (
		pl *llm.ProviderLimit
		ee *llm.EndError
	)
	switch {
	case errors.As(err, &pl):
		return res, state.ReasonProvider, err
	case st.stop != "":
		// A program that was stopped may report its own error result; the stop is the reason the session ended.
		return res, st.stop, nil
	case errors.As(err, &ee):
		return res, ee.Reason, err
	}

	return res, state.ReasonCrash, err
}

// cmpOrDur returns d, or def when d is zero.
func cmpOrDur(d, def time.Duration) time.Duration {
	if d != 0 {
		return d
	}
	return def
}
