package engine

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/resource"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

// PauseGroups pauses each group a PauseOrder names after its current step; the Resource Manager calls it.
func (m *Manager) PauseGroups(ctx context.Context, o resource.PauseOrder) error {
	gs, missing := m.groupsNamed(o.Groups)
	now := m.clk.Now()

	for _, g := range gs {
		g.mu.Lock()
		st := g.snap
		g.mu.Unlock()
		if st.Stage != StageBuild && st.Stage != StageTestReview {
			continue
		}

		lp := LimitPause{Link: o.Link}
		switch o.Reason {
		case "window_full":
			if m.otherLinkUsable(g, o.Link, now) {
				continue
			}
			lp.Cause, lp.Until = "plan_window", o.Until.Add(time.Minute)
		case "spend_ceiling":
			lp.Cause = "spending"
		case "spend_ceiling_day":
			lp.Cause, lp.Until = "spending", o.Until
			if lp.Until.IsZero() {
				lp.Until = nextMidnight(now)
			}
		case "group_tokens":
			lp.Cause = "group_tokens"
		default:
			return fmt.Errorf("pause order has the unknown reason %q", o.Reason)
		}

		g.enqueue(Input{Kind: InLimitPause, Time: now, Data: mustJSON(lp)})
	}

	if len(missing) > 0 {
		return fmt.Errorf("the engine does not host group %s", strings.Join(missing, ", "))
	}

	return nil
}

// NudgeGroups sends the token-cap nudge to the running agents of each named group; it changes no state.
func (m *Manager) NudgeGroups(ctx context.Context, o resource.NudgeOrder) error {
	gs, missing := m.groupsNamed(o.Groups)
	for _, g := range gs {
		g.send(task.Command{Kind: task.CmdNudge})
	}
	if len(missing) > 0 {
		return fmt.Errorf("the engine does not host group %s", strings.Join(missing, ", "))
	}
	return nil
}

// Answer answers the blocked or token-paused groups of a plan for `run (id) --approve` or `--deny`: every blocked group
// when labels is empty, otherwise the groups those labels name.
func (m *Manager) Answer(plan string, labels []string, approve bool, request string) error {
	_, err := m.AnswerNaming(plan, labels, approve, request)
	return err
}

// AnswerNaming is Answer; it also returns the labels of the approved groups that were blocked on a risky change, which
// are the ones of level high or critical.
func (m *Manager) AnswerNaming(plan string, labels []string, approve bool, request string) ([]string, error) {
	gs := m.byPlan(plan, labels...)
	if len(gs) == 0 {
		return nil, &codeError{CodeUnknownTarget, "no live group " + groupsNamedIn(plan, labels)}
	}

	var (
		answered int
		risky    []string
	)

	for _, g := range gs {
		ok, wasRisky := m.answerGroup(g, approve, request)
		if ok {
			answered++
		}
		if ok && wasRisky && approve {
			risky = append(risky, g.spec.Label)
		}
	}
	if answered == 0 {
		return nil, &codeError{CodeNotBlocked, groupsNamedIn(plan, labels) + " is not waiting for an answer"}
	}

	return risky, nil
}

// answerGroup sends the answer to one group and reports whether the group was waiting for it, and whether it was blocked
// on a risky change.
func (m *Manager) answerGroup(g *group, approve bool, request string) (answered, risky bool) {
	g.mu.Lock()
	st, ask := g.snap, g.ask
	g.mu.Unlock()

	answer := "deny"
	if approve {
		answer = "approve"
	}

	switch {
	case st.Stage == StageBlocked && st.Block.Reason == state.ReasonNeedsScope:
		sa := ScopeAnswer{Answer: answer}
		if ask != nil {
			sa.Paths, sa.Reason = ask.Paths, ask.Reason
		}
		g.enqueue(Input{Kind: InScopeAnswer, Request: request, Data: mustJSON(sa)})
	case st.Stage == StageBlocked && st.Block.Reason == state.ReasonRiskyChange:
		g.enqueue(Input{Kind: InContinue, Request: request, Data: mustJSON(ContinueData{Answer: answer})})
		if !approve {
			m.awaitDropped(g)
		}

		return true, true
	case st.Stage == StagePaused && st.Pause.Cause == "group_tokens" && approve:
		if m.d.Resource != nil {
			m.d.Resource.GrantGroupTokens(g.spec.ID)
		}
		g.enqueue(Input{Kind: InContinue, Request: request, Data: mustJSON(ContinueData{Answer: answer})})
	default:
		return false, false
	}

	return true, false
}

// awaitDropped waits for a denied group to end, then blocks its dependents as abandon does.
func (m *Manager) awaitDropped(g *group) {
	select {
	case <-g.done:
		m.blockDependents(g)
	case <-time.After(10 * time.Second):
		m.report(g, fmt.Errorf("group %s did not stop in time", g.spec.Label))
	}
}

// Override gives a group whose tries ran out one final try of the given minutes, for `run (id) --override`; it returns
// once the group has applied or refused it.
func (m *Manager) Override(ctx context.Context, plan, label, request string, minutes int) error {
	if label == "" {
		return &codeError{CodeRunRefused, "an override names one group: give its label after the plan id"}
	}

	gs := m.byPlan(plan, label)
	loaded := len(gs) > 0
	if !loaded {
		var err error
		if gs, err = m.wakeParked(ctx, plan, label); err != nil {
			return err
		}
	}

	g := gs[0]
	if loaded {
		if err := overridable(g); err != nil {
			return err
		}
	}

	request = cmp.Or(request, fmt.Sprintf("override-%s-%d", g.spec.ID, g.now().UnixNano()))

	return m.awaitContinues(ctx, []pendingContinue{m.queueInput(g, Input{
		Kind: InOverride, Request: request, Data: mustJSON(OverrideData{Minutes: minutes}),
	})})
}

// overridable returns the refusal for a live group that cannot take an override now, or nil.
func overridable(g *group) error {
	g.mu.Lock()
	st := g.snap
	g.mu.Unlock()

	switch {
	case st.Stage == StageDone && st.Outcome == "failed":
		return &codeError{CodeGroupFailed, g.spec.Label + " failed for good: plan it again to run it fresh"}
	case st.Counters.Overrides >= 1:
		return &codeError{CodeRunRefused, g.spec.Label + " was not continued: its override was already used"}
	case st.Stage == StageBlocked && st.Block.Reason == state.ReasonRetries:
		if st.Block.Until.IsZero() || !g.now().Before(st.Block.Until) {
			return &codeError{CodeRunRefused, g.spec.Label + " was not continued: the override window has passed"}
		}

		return nil
	}

	return &codeError{CodeNotBlocked, g.spec.Label + " is not waiting for an override"}
}

// Abandon drops the group a plan id and label name for good: it is stopped, Done with outcome abandoned, discarded, and
// each dependent blocks.
func (m *Manager) Abandon(ctx context.Context, plan, label string) (string, error) {
	var gs []*group
	if label != "" {
		gs = m.byPlan(plan, label)
	}
	if len(gs) == 0 {
		return "", &codeError{CodeUnknownTarget, "no live group " + groupsNamedIn(plan, []string{label})}
	}

	g := gs[0]
	g.enqueue(Input{Kind: InAbandon, Data: mustJSON(AbandonData{Group: g.spec.ID})})

	select {
	case <-g.done:
	case <-ctx.Done():
		return g.r.id, ctx.Err()
	case <-time.After(10 * time.Second):
		return g.r.id, fmt.Errorf("group %s did not stop in time", g.spec.Label)
	}

	m.blockDependents(g)
	return g.r.id, nil
}

// blockDependents blocks each group that depends on a group that ended without landing, naming it, and takes it off its
// epic's landing order.
func (m *Manager) blockDependents(g *group) {
	m.mu.Lock()
	r := g.r

	var dependents []*group
	for id, s := range r.specs {
		if !slices.Contains(s.Depends, g.spec.ID) {
			continue
		}
		if d := r.groups[id]; d != nil && !r.finished[id] {
			dependents = append(dependents, d)
		} else if d == nil && !r.finished[id] {
			r.depBlocked[id] = g.spec.Label
		}
	}
	if p, ok := r.landing[g.spec.Epic]; ok {
		p.Order = slices.DeleteFunc(slices.Clone(p.Order), func(l string) bool {
			return l == g.spec.Label
		})
		r.landing[g.spec.Epic] = p
	}
	m.mu.Unlock()

	for _, d := range dependents {
		d.enqueue(Input{Kind: InAbandon, Data: mustJSON(AbandonData{Group: g.spec.ID})})
	}

	m.groupChanged(g)
}

// LandingPlan returns the landing plan of the epic a group belongs to, for the Commit Handler.
func (m *Manager) LandingPlan(group string) (LandingPlan, bool) {
	gs, _ := m.groupsNamed([]string{group})
	if len(gs) == 0 {
		return LandingPlan{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return gs[0].r.landingFor(gs[0].spec.Epic)
}

// groupsNamed returns the live groups the names identify, and the names that match none.
func (m *Manager) groupsNamed(names []string) ([]*group, []string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var (
		out     []*group
		missing []string
	)

	for _, n := range names {
		var found *group
		for _, g := range m.groups {
			if g.spec.ID == n || g.spec.Label == n {
				found = g
			}
		}
		if found == nil {
			missing = append(missing, n)
			continue
		}

		out = append(out, found)
	}

	return out, missing
}

// otherLinkUsable reports whether the group's current agent has another link in its chain that is not limited.
func (m *Manager) otherLinkUsable(g *group, limited string, now time.Time) bool {
	if m.d.Chain == nil {
		return false
	}

	var providers map[string]state.LinkRecord
	if m.d.Machine != nil {
		if rec, err := state.LoadMachine[state.ProvidersBody](m.d.Machine, state.KindProviders); err == nil {
			providers = rec.Body.Links
		}
	}

	for _, l := range m.d.Chain(g.spec.ID) {
		if l == limited {
			continue
		}
		if p, ok := providers[l]; ok && p.ResetAt.After(now) {
			continue
		}
		return true
	}

	return false
}

// continueTarget handles a run-continue event: an answer, or a plain continue of every held group the target names.
func (m *Manager) continueTarget(ctx context.Context, rc *bus.RunContinue, run string) error {
	if rc.Answer == "override" {
		return m.Override(ctx, rc.Target, firstOf(rc.Groups), string(rc.Request), rc.Minutes)
	}
	if rc.Answer != "" {
		risky, err := m.AnswerNaming(rc.Target, rc.Groups, rc.Answer == "approve", string(rc.Request))
		if len(risky) > 0 {
			m.keepRisky(string(rc.Request), risky)
		}

		return err
	}

	gs := m.byPlan(rc.Target, rc.Groups...)
	if len(gs) == 0 {
		m.mu.Lock()
		if r := m.runs[rc.Target]; r != nil {
			for _, g := range r.groups {
				if len(rc.Groups) == 0 || slices.Contains(rc.Groups, g.spec.Label) {
					gs = append(gs, g)
				}
			}
		}
		m.mu.Unlock()
	}
	if len(gs) == 0 {
		return &codeError{CodeUnknownTarget, "no live group " + groupsNamedIn(rc.Target, rc.Groups) + " or run named " + rc.Target}
	}

	var pending []pendingContinue

	for _, g := range gs {
		g.mu.Lock()
		st := g.snap
		g.mu.Unlock()
		if st.Stage != StagePaused && st.Stage != StageBlocked {
			continue
		}
		if st.Stage == StageBlocked && st.Block.Reason == state.ReasonDependency {
			if dep := m.undoneDep(g); dep != "" {
				m.publishRefusal(g, string(rc.Request), "dependency "+dep+" is not Done")
				continue
			}
		}

		pending = append(pending, m.queueContinue(g, string(rc.Request)+"/"+g.spec.ID))
	}

	return m.awaitContinues(ctx, pending)
}

// TakeRisky returns, and forgets, the labels of the high or critical groups the request's approval landed.
func (m *Manager) TakeRisky(request string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	l := m.risky[request]
	delete(m.risky, request)

	return l
}

// keepRisky holds the labels of the high or critical groups a request's approval landed, until TakeRisky takes them.
func (m *Manager) keepRisky(request string, labels []string) {
	m.mu.Lock()
	m.risky[request] = labels
	m.mu.Unlock()
}

// groupsNamedIn words the groups a plan id and labels name, for a refusal.
func groupsNamedIn(plan string, labels []string) string {
	if len(labels) == 0 || labels[0] == "" {
		return "of plan " + plan
	}

	return strings.Join(labels, ", ") + " of plan " + plan
}

// firstOf returns the first element of a list, or an empty string.
func firstOf(l []string) string {
	if len(l) == 0 {
		return ""
	}

	return l[0]
}

// undoneDep returns the label of a dependency of the group that is not Done.
func (m *Manager) undoneDep(g *group) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.firstUnfinishedDep(g.r, g.spec.ID)
}

// publishRefusal publishes a request.refused fact for a command that was not applied.
func (m *Manager) publishRefusal(g *group, request, why string) {
	if request == "" {
		return
	}

	_ = m.d.Bus.Publish(Event{
		Type:    bus.TypeRunStatus,
		Subtype: "request.refused",
		Run:     bus.ID(g.r.id),
		Group:   bus.ID(g.spec.ID),
		Data: &bus.RequestRefused{
			RequestRef: bus.RequestRef{Request: bus.ID(request)},
			Why:        why,
		},
	})
}

// taskStart is the command that starts or restarts a group's pipeline.
func taskStart() task.Command { return task.Command{Kind: task.CmdStart} }

// stopCommand is the command that cancels every live session of a group.
func stopCommand(cause string) task.Command {
	return task.Command{Kind: task.CmdStop, Cause: cause}
}

// sessionCrash is the session.end the dead-session rule saves for a session found live after a crash.
func sessionCrash(agent, session, taskID string, stage Stage) SessionEndData {
	d := SessionEndData{}
	d.Agent, d.Task, d.Reason = agent, taskID, string(state.ReasonCrash)
	d.Restart = true
	d.Stage, d.Session = string(stage), busID(session)
	if agent == "builder" && taskID != "" {
		d.Unfinished = &state.UnfinishedEdits{Task: taskID, Files: []string{}}
	}
	return d
}
