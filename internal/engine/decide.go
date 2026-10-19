package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// calc is the working copy of one call of Next.
type calc struct {
	s      State
	in     Input
	eff    Input // the input the stage-move rows read; a session end can stand for a stage result
	l      Limits
	from   Stage // the stage when the input arrived
	orders []Order
	emit   []Event
	// dropped holds a line for each fact that stood for no event.
	dropped []string
	refuse  *bus.RequestRefused
	// scopeBlock is set when the scope rules did not deny a request, so the block rows block the group.
	scopeBlock *ScopeAsk
	// exhausted names the failure that found the retry budget empty; the block rows then hold the group for an override.
	exhausted string
}

// allTasksDone reports whether every task is done.
func (c *calc) allTasksDone() bool { return c.firstOpenTask() == "" }

// firstOpenTask returns the first task not done.
func (c *calc) firstOpenTask() string {
	for _, t := range c.s.Tasks {
		if t.State != "done" {
			return t.ID
		}
	}
	return ""
}

// tick adds working time and raises the wall extension; only a live builder in Build counts.
func (c *calc) tick() {
	s := &c.s
	if s.Stage != StageBuild {
		return
	}
	if c.builderLive() && !s.LastEventAt.IsZero() && c.in.Time.After(s.LastEventAt) {
		s.Counters.WallUsedS += int64(c.in.Time.Sub(s.LastEventAt) / time.Second)
	}
	if c.l.GroupWallMin > 0 && s.Counters.WallUsedS >= int64(c.l.GroupWallMin)*60 {
		s.Counters.WallExtended = true
	}
}

// builderLive reports whether a builder session holds a task.
func (c *calc) builderLive() bool {
	for _, t := range c.s.Tasks {
		if t.State == "claimed" && t.Session != "" {
			return true
		}
	}
	return false
}

// inStage applies the in-stage rows: the changes an input makes without moving the group.
func (c *calc) inStage() bool {
	s := &c.s
	switch c.in.Kind {
	case InFact:
		d := decode[FactData](c.in.Data)
		if e, err := factEvent(s.Run, s.Group, d.Subtype, d.Data); err == nil {
			c.emit = append(c.emit, e)
		} else {
			c.dropped = append(c.dropped, fmt.Sprintf("fact %q dropped: %v", d.Subtype, err))
		}
		return true
	case InProgress:
		if s.Stage == StageDone {
			return false
		}
		for _, r := range decode[ProgressData](c.in.Data).Live {
			c.setResume(r.Agent, &r)
		}
		return true
	case InLoopRestart:
		if s.Stage == StageBuild || s.Stage == StageTestReview {
			c.waive("restart")
			c.enterStage(s.Stage, stageData{Task: c.firstOpenTask(), Retry: "loop"})
			return true
		}
	case InSessionStart:
		return c.sessionStart()
	case InSessionEnd:
		return c.sessionEnd()
	case InTaskDone:
		return c.taskDone()
	case InGateFail:
		return c.gateFail()
	case InTimerExtend:
		return c.timerExtend()
	case InScopeAsk:
		return c.scopeAsk()
	case InLimitPause:
		if (s.Stage == StageBuild || s.Stage == StageTestReview) && c.anyLive() {
			d := decode[LimitPause](c.in.Data)
			s.Pause.After, s.Pause.Cause = true, d.Cause
			s.Pause.Link, s.Pause.Until = d.Link, d.Until
			return true
		}
	}

	return false
}

// setResume replaces the resume reference of an agent, or removes it when ref is nil.
func (c *calc) setResume(agent string, ref *state.ResumeRef) {
	c.s.Resume = slices.DeleteFunc(c.s.Resume, func(r state.ResumeRef) bool {
		return r.Agent == agent
	})
	if ref != nil {
		c.s.Resume = append(c.s.Resume, *ref)
	}
}

// enterStage queues an enter_stage order that carries the saved resume references and the extra detail.
func (c *calc) enterStage(stage Stage, d stageData) {
	if d.Unfinished == nil {
		d.Unfinished = c.s.Unfinished
	}
	o := c.order(OrderEnterStage, stage, d)
	o.Resume = slices.Clone(c.s.Resume)
}

// order queues an order with its key; data is encoded as the order's data.
func (c *calc) order(k OrderKind, stage Stage, data any) *Order {
	o := Order{Kind: k, Stage: stage}
	if data != nil {
		o.Data, _ = json.Marshal(data)
	}

	h := sha256.Sum256([]byte(fmt.Sprintf(
		"%s|%d|%s|%d|%s|%d",
		k,
		c.s.Epoch,
		c.in.Kind,
		c.in.Time.UnixNano(),
		c.in.Request,
		len(c.orders),
	)))
	o.Key = fmt.Sprintf("%s-%s-e%d-%s", c.s.Group, k, c.s.Epoch, hex.EncodeToString(h[:4]))
	c.orders = append(c.orders, o)
	return &c.orders[len(c.orders)-1]
}

// sessionStart records a session that began.
func (c *calc) sessionStart() bool {
	s := &c.s
	d := decode[SessionStartData](c.in.Data)
	switch s.Stage {
	case StageBuild, StageTestReview:
	case StagePaused, StageBlocked, StageDone:
		c.emitSession(d.SessionStart)
		return true
	default:
		return false
	}

	switch d.Agent {
	case "builder":
		for i := range s.Tasks {
			if (d.Task != "" && s.Tasks[i].ID == d.Task) || (d.Task == "" && s.Tasks[i].State != "done") {
				s.Tasks[i].State, s.Tasks[i].Session = "claimed", string(d.Session)
				break
			}
		}
	case "tester":
		s.TestReview.Tester.Session, s.TestReview.Tester.Result = string(d.Session), ""
	case "reviewer":
		s.TestReview.Reviewer.Session, s.TestReview.Reviewer.Result = string(d.Session), ""
	}

	c.emitSession(d.SessionStart)
	return true
}

// charge spends one retry for a true failure; with none left it marks the budget empty for the block rows.
func (c *calc) charge(cause string) bool {
	s := &c.s
	if s.Counters.RetriesLeft <= 0 {
		c.exhausted = cause
		return false
	}

	s.Counters.RetriesLeft--
	c.emitStatus(&bus.RetrySpent{
		GroupFacts: bus.GroupFacts{Stage: string(s.Stage), Reason: cause},
		Left:       s.Counters.RetriesLeft,
	})

	return true
}

// waive records a failure that is Komodo's own fault: it costs the group no retry.
func (c *calc) waive(cause string) {
	c.emitStatus(&bus.RetryWaived{
		GroupFacts: bus.GroupFacts{Stage: string(c.s.Stage), Reason: cause},
		Left:       c.s.Counters.RetriesLeft,
	})
}

// emitSession queues a session.start event with the stage filled in.
func (c *calc) emitSession(e bus.SessionStart) {
	if e.Stage == "" {
		e.Stage = string(c.s.Stage)
	}
	c.emitStatus(&e)
}

// emitStatus queues a run-status event.
func (c *calc) emitStatus(p bus.Payload) { c.emit = append(c.emit, c.event(bus.TypeRunStatus, p)) }

// event wraps a payload as an event of this group.
func (c *calc) event(t bus.Type, p bus.Payload) Event {
	return Event{
		Type:    t,
		Subtype: p.EventKey().Subtype,
		Run:     bus.ID(c.s.Run),
		Group:   bus.ID(c.s.Group),
		Data:    p,
	}
}

// taskDone marks a task done and clears what only an unfinished task needs.
func (c *calc) taskDone() bool {
	s := &c.s

	d := decode[bus.TaskDone](c.in.Data)
	if s.Stage == StagePaused || s.Stage == StageBlocked || s.Stage == StageDone {
		c.emitStatus(&d)
		return true
	}
	if s.Stage != StageBuild {
		return false
	}
	if t := c.taskByID(d.Task, ""); t != nil {
		t.State, t.Session, t.GateFailures = "done", "", 0
	}
	if s.Unfinished != nil && (d.Task == "" || s.Unfinished.Task == d.Task) {
		s.Unfinished = nil
	}

	s.Pause = state.PauseState{}
	if d.Stage == "" {
		d.Stage = string(s.Stage)
	}

	c.emitStatus(&d)
	return true
}

// taskByID returns the task with the id, or the one a session holds when the id is empty.
func (c *calc) taskByID(id, session string) *state.TaskState {
	for i := range c.s.Tasks {
		t := &c.s.Tasks[i]
		if id != "" && t.ID == id {
			return t
		}
	}

	for i := range c.s.Tasks {
		t := &c.s.Tasks[i]
		if t.State == "claimed" && (session == "" || t.Session == session) {
			return t
		}
	}

	return nil
}

// gateFail counts a failed completion gate; the third failure is a retry the Task Manager ends the session for.
func (c *calc) gateFail() bool {
	s := &c.s
	if s.Stage != StageBuild {
		return false
	}

	d := decode[bus.TaskDone](c.in.Data)
	t := c.taskByID(d.Task, "")
	if t == nil {
		return false
	}

	t.GateFailures++
	if t.GateFailures >= 3 {
		c.emitStatus(&bus.TaskRetry{
			GroupFacts: bus.GroupFacts{
				Stage:  string(s.Stage),
				Reason: string(state.ReasonCheckFail),
			},
			Task:    t.ID,
			Attempt: max(c.l.GroupRetries-s.Counters.RetriesLeft, 0) + 1,
		})
		t.GateFailures = 0
	}

	return true
}

// timerExtend grants the one wall-time extension of the group, or denies it.
func (c *calc) timerExtend() bool {
	s := &c.s
	if s.Stage != StageBuild {
		return false
	}

	req := decode[struct {
		Limit time.Duration `json:"limit"`
	}](c.in.Data)
	grant := struct {
		Extra time.Duration `json:"extra"`
	}{}
	if !s.Counters.WallExtended {
		s.Counters.WallExtended = true
		grant.Extra = req.Limit
	}

	o := c.order(OrderExtend, StageBuild, grant)
	o.Re = c.in.Request
	return true
}

// anyLive reports whether any session of the group is running.
func (c *calc) anyLive() bool {
	for _, r := range []state.RoundState{c.s.TestReview.Tester, c.s.TestReview.Reviewer} {
		if r.Session != "" && r.Result == "" {
			return true
		}
	}
	return c.builderLive()
}

// stageData is the data of an enter_stage order.
type stageData struct {
	Task       string                 `json:"task,omitempty"`       // the first task not done, for Build
	Findings   string                 `json:"findings,omitempty"`   // the exact output of a failed round
	Items      []string               `json:"items,omitempty"`      // the failing items of a failed round
	Retry      string                 `json:"retry,omitempty"`      // the reason a fresh session is ordered
	Agent      string                 `json:"agent,omitempty"`      // the one agent to start, when not the whole stage
	Wake       string                 `json:"wake,omitempty"`       // timer or run-continue, for a resumed pause
	Unfinished *state.UnfinishedEdits `json:"unfinished,omitempty"` // edits a session left in the worktree
	Landing    *LandingPlan           `json:"landing,omitempty"`    // the epic's landing plan, for a Commit order
}

// NewState returns the state of a group before its first input.
func NewState(run string, spec GroupSpec, configHash string) State {
	s := State{Run: run, Group: spec.ID, ConfigHash: configHash}
	for _, t := range spec.Tasks {
		s.Tasks = append(s.Tasks, state.TaskState{ID: t, State: "queued"})
	}
	normalize(&s)
	return s
}

// Next applies one input to one group's state; it reads nothing, writes nothing and never fails.
func Next(s0 State, in Input, l Limits) Result {
	// maxRequests is the most request identifiers a group record keeps.
	const maxRequests = 64
	if in.Epoch != 0 && in.Epoch < s0.Epoch {
		return Result{State: s0, Stale: true}
	}
	if in.Request != "" && slices.Contains(s0.Requests, in.Request) {
		return Result{State: s0, Dup: true}
	}

	c := &calc{s: cloneState(s0), in: in, eff: in, l: l}
	c.from = c.s.Stage
	if in.Request != "" {
		c.s.Requests = append(c.s.Requests, in.Request)
		if n := len(c.s.Requests) - maxRequests; n > 0 {
			c.s.Requests = slices.Clone(c.s.Requests[n:])
		}
	}

	c.tick()
	a := c.inStage()

	b := c.move()
	if !a && !b {
		r := Result{State: s0, Refused: true}
		if c.refuse != nil {
			r.Why = c.refuse.Why
			r.Emit = []Event{c.event(bus.TypeRunStatus, c.refuse)}
		}
		return r
	}
	if in.Time.After(c.s.LastEventAt) {
		c.s.LastEventAt = in.Time
	}

	for _, o := range c.orders {
		args, _ := json.Marshal(o)
		c.s.ActionsOpen = append(c.s.ActionsOpen, state.OpenAction{ID: o.Key, Kind: string(o.Kind), Args: args})
	}

	return Result{State: c.s, Orders: c.orders, Emit: c.emit, Dropped: c.dropped}
}

// cloneState returns a deep copy of s with no nil slice or map.
func cloneState(s State) State {
	b, _ := json.Marshal(s)
	var out State
	_ = json.Unmarshal(b, &out)
	normalize(&out)
	return out
}

// normalize replaces every nil slice and map of a group state with an empty one.
func normalize(s *State) {
	if s.Counters.Blocks == nil {
		s.Counters.Blocks = map[string]int{}
	}
	if s.Tasks == nil {
		s.Tasks = []state.TaskState{}
	}
	if s.TestReview.Findings == nil {
		s.TestReview.Findings = []state.Finding{}
	}
	if s.TestReview.NotMet == nil {
		s.TestReview.NotMet = []string{}
	}
	if s.TestReview.Failed == nil {
		s.TestReview.Failed = []string{}
	}
	if s.ActionsOpen == nil {
		s.ActionsOpen = []state.OpenAction{}
	}
	if s.Resume == nil {
		s.Resume = []state.ResumeRef{}
	}
	if s.Owed == nil {
		s.Owed = []string{}
	}
	if s.Requests == nil {
		s.Requests = []string{}
	}
	if s.ScopeAdded == nil {
		s.ScopeAdded = []string{}
	}
}

// decode reads the input's data into a T; a missing or broken body gives the zero value.
func decode[T any](raw json.RawMessage) T {
	var v T
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &v)
	}
	return v
}

// working reports whether a stage is one a group works in.
func working(st Stage) bool {
	switch st {
	case StageIntake, StageBuild, StageTestReview, StageCommit, StageCleanup:
		return true
	}
	return false
}

// FactSlot reports whether a Task Manager fact has a place in the event history: it is an input of its own name, or it
// builds a registered event. It returns the reason when the fact would be dropped.
func FactSlot(subtype string) error {
	if _, ok := stateFacts[subtype]; ok {
		return nil
	}
	_, err := factEvent("", "", subtype, nil)
	return err
}

// factEvent builds the event a Task Manager fact stands for; it fails when the fact has no registered shape or its data does
// not decode as that shape.
func factEvent(run, group, subtype string, data json.RawMessage) (Event, error) {
	typ := bus.TypeRunStatus
	if subtype == "limit.warn" || subtype == "usage" {
		typ = bus.TypeResource
	}
	if len(data) == 0 {
		data = json.RawMessage(`{}`)
	}

	b, err := json.Marshal(map[string]any{
		"type":    typ,
		"subtype": subtype,
		"run":     run,
		"group":   group,
		"time":    time.Time{},
		"data":    data,
	})
	if err != nil {
		return Event{}, fmt.Errorf("encode the fact: %w", err)
	}

	var e Event
	if err := json.Unmarshal(b, &e); err != nil {
		return Event{}, err
	}

	return e, nil
}
