package engine

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

// Channels connect the Runtime Manager to one Task Manager goroutine, seen from the Runtime Manager's side.
type Channels struct {
	Commands chan<- task.Command // orders go down
	Replies  <-chan task.Message // messages come back; closed when the Task Manager ends
}

// stateFacts are the fact subtypes that are inputs of their own name.
var stateFacts = map[string]InputKind{
	"ready":         InReady,
	"session.start": InSessionStart,
	"session.end":   InSessionEnd,
	"task.done":     InTaskDone,
	"gate.fail":     InGateFail, "stage.done": InStageDone, "result.fail": InResultFail,
}

// inputsFromMessage turns one Task Manager message into the inputs it stands for; epochOf gives the epoch a stage was last
// entered under.
func inputsFromMessage(group string, m task.Message, epochOf func(Stage) uint32, now time.Time) []Input {
	mk := func(k InputKind, epoch uint32, req string, data any) Input {
		in := Input{Kind: k, Group: group, Epoch: epoch, Time: now, Request: req}
		if data != nil {
			in.Data = mustJSON(data)
		}
		return in
	}

	var out []Input

	switch m.Kind {
	case task.MsgState:
		if m.State == nil {
			return nil
		}

		ep := epochOf(m.State.Progress.Stage)
		if hs := m.State.Progress.Handles; len(hs) > 0 {
			var live []state.ResumeRef
			for agent, h := range hs {
				live = append(live, state.ResumeRef{Agent: agent, Handle: h.Value, Link: string(h.Link)})
			}
			out = append(out, mk(InProgress, 0, "", ProgressData{Live: live}))
		}

		for i, f := range m.State.Facts {
			at := f.At
			if at.IsZero() {
				at = now
			}

			req := m.ID + "#" + strconv.Itoa(i)
			if k, ok := stateFacts[f.Subtype]; ok {
				in := mk(k, ep, req, nil)
				in.Data, in.Time = f.Data, at
				out = append(out, in)
				continue
			}

			in := mk(InFact, 0, "", FactData{Subtype: f.Subtype, Data: f.Data})
			in.Time = at
			out = append(out, in)
		}
	case task.MsgEscalate:
		e := m.Escalation
		if e == nil {
			return nil
		}
		if e.Kind == task.EscScope {
			a := decode[ScopeAsk](e.Detail)
			a.Escalation = e.ID
			out = append(out, mk(InScopeAsk, 0, m.ID, a))
		} else {
			out = append(out, mk(InFault, 0, m.ID, FaultData{Reason: e.Reason, Detail: string(e.Detail)}))
		}
	case task.MsgTimer:
		if m.Timer != nil {
			out = append(out, mk(InTimerExtend, 0, m.ID, m.Timer))
		}
	case task.MsgEnded:
		if m.Ended == nil {
			return nil
		}
		switch m.Ended.Result {
		case "closed":
			out = append(out, mk(InGroupClosed, 0, m.ID, nil))
		case "failed":
			out = append(out, mk(InFault, 0, m.ID, FaultData{Reason: string(state.ReasonCrash), Detail: m.Ended.Error}))
		}
	}

	return out
}

// commandFor turns an order into the command a Task Manager reads; ok is false for an order the Runtime Manager performs
// itself.
func commandFor(o Order, s State) (task.Command, bool) {
	switch o.Kind {
	case OrderStartGroup:
		return task.Command{Kind: task.CmdStart}, true
	case OrderEnterStage:
		d := decode[stageData](o.Data)
		if d.Wake != "" {
			return task.Command{Kind: task.CmdContinue, Wake: &task.Wake{Reason: d.Wake, At: time.Now()}}, true
		}
		return task.Command{Kind: task.CmdStart}, true
	case OrderCancel:
		cause := decode[map[string]string](o.Data)["cause"]
		switch cause {
		case "abandon", "pause_timeout":
		default:
			cause = "stop"
		}
		return task.Command{Kind: task.CmdStop, Cause: cause}, true
	case OrderHold:
		return task.Command{Kind: task.CmdPause, Link: s.Pause.Link, Until: s.Pause.Until, Cause: s.Pause.Cause}, true
	case OrderExtend:
		g := decode[struct {
			Extra time.Duration `json:"extra"`
		}](o.Data)
		return task.Command{Kind: task.CmdContinue, Re: o.Re, Grant: &task.TimerGrant{Extra: g.Extra}}, true
	case OrderScope:
		a := decode[ScopeAnswer](o.Data)
		return task.Command{
			Kind:   task.CmdContinue,
			Re:     o.Re,
			Answer: &task.Answer{Escalation: o.Re, Action: a.Answer, Approved: a.Paths},
		}, true
	}

	return task.Command{}, false
}

// rawOrder decodes the order saved in an open action.
func rawOrder(a state.OpenAction) Order {
	var o Order
	_ = json.Unmarshal(a.Args, &o)
	return o
}
