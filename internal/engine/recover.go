package engine

import (
	"errors"
	"fmt"
	"slices"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/lease"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// recoverInit continues a group from its saved state: lease, open orders, dead sessions and timers. It reports whether the
// loop should run.
func (g *group) recoverInit(rec state.Record) bool {
	g.st, g.rev, g.created = rec.State, rec.Header.Rev, true
	g.mu.Lock()

	g.snap = g.st
	if working(g.st.Stage) || g.st.Stage == StagePaused || g.st.Stage == StageBlocked {
		g.epochs[workingOf(g.st)] = g.st.Epoch
	}
	g.mu.Unlock()
	if g.st.Stage == StageDone {
		return false
	}

	l, err := g.m.d.Leases.Grant(g.ctx, lease.GrantRequest{
		Repo:      g.r.start.Repo,
		Group:     g.spec.ID,
		Run:       g.r.id,
		Branch:    g.branchOf(),
		Worktree:  g.worktreeOf(),
		ClonePath: g.r.start.Root,
	})
	if err != nil {
		if errors.Is(err, lease.ErrLeased) {
			g.endLost()
		} else {
			g.m.leaseRefused(g, err)
		}
		return false
	}

	g.l = l
	ns := cloneState(g.st)
	ns.Run, ns.Fence, ns.LastEventAt = g.r.id, uint64(l.Fence), g.now()
	if err := g.save(ns); err != nil {
		g.m.report(g, fmt.Errorf("save recovered %s: %w", g.spec.Label, err))
		_ = g.m.d.Leases.Release(g.ctx, l)
		return false
	}

	g.r.firstSave()
	g.after()

	switch g.st.Stage {
	case StagePaused, StageBlocked:
		g.dropOpenOrders()
	case StageCommit:
		g.handle(Input{
			Kind:    InContinue,
			Group:   g.spec.ID,
			Time:    g.now(),
			Request: "recover-" + g.r.id + "-" + g.spec.ID,
			Data:    mustJSON(ContinueData{Recovery: true}),
		})
	default:
		g.resumeWork()
	}

	return true
}

// endLost ends a group whose lease another holder took, with outcome lost.
func (g *group) endLost() {
	res := Next(g.st, Input{Kind: InLeaseLost, Group: g.spec.ID, Time: g.now()}, g.r.limits)
	if res.Refused || g.save(res.State) != nil {
		return
	}
	g.publish(res.Emit)
	g.after()
}

// dropOpenOrders marks every open order done without running it; a held group has nothing running to order.
func (g *group) dropOpenOrders() {
	ns := cloneState(g.st)
	ns.ActionsOpen = []state.OpenAction{}
	if err := g.save(ns); err != nil {
		g.m.report(g, fmt.Errorf("clear open orders of %s: %w", g.spec.Label, err))
	}
}

// resumeWork applies the dead-session rule and reruns the open orders of a group in a working stage.
func (g *group) resumeWork() {
	dead := g.deadSessions()
	if len(dead) > 0 {
		ns := cloneState(g.st)
		ns.ActionsOpen = slices.DeleteFunc(ns.ActionsOpen, func(a state.OpenAction) bool {
			return a.Kind == string(OrderEnterStage) || a.Kind == string(OrderStartGroup)
		})
		if g.save(ns) == nil {
			for _, in := range dead {
				g.handle(in)
			}
		}

		return
	}

	open := make([]Order, 0, len(g.st.ActionsOpen))
	for _, a := range g.st.ActionsOpen {
		open = append(open, rawOrder(a))
	}
	if !slices.ContainsFunc(open, func(o Order) bool {
		return o.Kind == OrderEnterStage || o.Kind == OrderStartGroup
	}) &&
		g.st.Stage != StageDone {
		if g.ensureTM() == nil {
			g.send(taskStart())
		}
	}

	g.perform(open)
}

// deadSessions returns a session.end of reason crash for every session the state shows as live.
func (g *group) deadSessions() []Input {
	var out []Input

	mk := func(agent, session, task string) {
		out = append(
			out,
			Input{
				Kind:  InSessionEnd,
				Group: g.spec.ID,
				Time:  g.now(),
				Data:  mustJSON(sessionCrash(agent, session, task, g.st.Stage)),
			},
		)
	}

	for _, t := range g.st.Tasks {
		if t.State == "claimed" && t.Session != "" {
			mk("builder", t.Session, t.ID)
		}
	}

	for _, v := range []struct {
		name  string
		round state.RoundState
	}{{"qa", g.st.TestReview.QA}, {"reviewer", g.st.TestReview.Reviewer}} {
		if v.round.Session != "" && v.round.Result == "" {
			mk(v.name, v.round.Session, "")
		}
	}

	return out
}

// workingOf returns the working stage a state belongs to: its own, or the one a held group left.
func workingOf(s State) Stage {
	if s.Stage == StagePaused || s.Stage == StageBlocked {
		return s.Prior
	}
	return s.Stage
}
