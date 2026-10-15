package task

import "time"

// Channels connect a Task Manager goroutine to the Runtime Manager that started it.
type Channels struct {
	Commands <-chan Command // from the Runtime Manager
	Replies  chan<- Message // to the Runtime Manager; closed when Serve returns
}

// CommandKind names an instruction on the command channel.
type CommandKind string

// The command kinds.
const (
	CmdStart    CommandKind = "start"    // run the pipeline
	CmdPause    CommandKind = "pause"    // hold the group after its current step
	CmdContinue CommandKind = "continue" // an answer, a timer grant or a wake-up
	CmdStop     CommandKind = "stop"     // cancel live sessions and end
	CmdNudge    CommandKind = "nudge"    // add the token-cap nudge to each live session
	CmdRecheck  CommandKind = "recheck"  // run the free checks on the epic tip in the group's container
)

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

// The message kinds.
const (
	MsgEscalate MessageKind = "escalate" // a blocker or a request
	MsgTimer    MessageKind = "timer"    // a request for more wall time
	MsgState    MessageKind = "state"    // progress and the facts since the last report
	MsgEnded    MessageKind = "ended"    // Run, Resume or Continue returned
)

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
