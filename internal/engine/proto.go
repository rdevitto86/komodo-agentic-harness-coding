// Package engine holds the engine process and its control socket; this file holds only the socket's wire types.
package engine

import (
	"encoding/json"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// Event is the bus event a request or a reply carries.
type Event = bus.Event

// Request is one frame from the CLI to the engine.
type Request struct {
	// minted by the CLI; every reply repeats it
	ID string `json:"id"`
	// run, stop, status, abandon, replay, plan or clean
	Verb string `json:"verb"`
	// an epic label, a group label or a run id
	Target string `json:"target,omitempty"`
	// the repository root the command ran in; status covers this repository unless Flags.All is set
	Root  string `json:"root,omitempty"`
	Flags Flags  `json:"flags"`
	// the run-start, run-stop, run-continue or plan event the CLI publishes
	Event *Event `json:"event,omitempty"`
}

// Flags are the flags a verb may carry.
type Flags struct {
	All      bool      `json:"all,omitempty"`       // run --all, or status --all
	Approve  bool      `json:"approve,omitempty"`   // run (id) --approve
	Deny     bool      `json:"deny,omitempty"`      // run (id) --deny
	Wait     bool      `json:"wait,omitempty"`      // status --wait
	TimeoutS int       `json:"timeout_s,omitempty"` // status --wait: the longest wait, in seconds
	Schedule time.Time `json:"schedule,omitzero"`   // run (id) --schedule, an RFC 3339 timestamp
	DryRun   bool      `json:"dry_run,omitempty"`   // clean --dry-run
}

// Reply is one frame from the engine to the CLI.
type Reply struct {
	// the request's id
	ID string `json:"id"`
	// ok, refused, error or event
	Status string `json:"status"`
	// an error value from the code constants
	Code string `json:"code,omitempty"`
	// one sentence for a person
	Detail string `json:"detail,omitempty"`
	// the verb's answer, such as a status table or a blocked group's question
	Data json.RawMessage `json:"data,omitempty"`
	// set when Status is event
	Event *Event `json:"event,omitempty"`
	// the last frame for this request
	Final bool `json:"final"`
}

// The verbs a Request may name.
const (
	VerbRun     = "run"
	VerbStop    = "stop"
	VerbStatus  = "status"
	VerbAbandon = "abandon"
	VerbReplay  = "replay"
	VerbPlan    = "plan"
	VerbClean   = "clean"
)

// The values of Reply.Status.
const (
	StatusOK      = "ok"      // the request was accepted or answered
	StatusRefused = "refused" // the engine declined on purpose; Code says why
	StatusError   = "error"   // the request failed
	StatusEvent   = "event"   // a recorded event or a live fact; Event is set
)

// The values of Reply.Code.
const (
	CodeBadFrame       = "bad_frame"       // the line is not a valid Request; the connection stays open
	CodeUnknownTarget  = "unknown_target"  // the label or run id names nothing in the plan or the saved state
	CodeRunRefused     = "run_refused"     // the run was refused, such as a dependency cycle
	CodeNotBlocked     = "not_blocked"     // --approve or --deny named a group that is not waiting for an answer
	CodeEngineStopping = "engine_stopping" // the engine is past its exit check; the CLI starts a new engine and resends
)
