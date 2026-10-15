// Package engine holds the engine process and its control socket; this file holds only the socket's wire types.
package engine

import (
	"encoding/json"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// Event is the bus event a request or a reply carries.
type Event = bus.Event

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

// Request is one frame from the CLI to the engine.
type Request struct {
	ID     string `json:"id"`               // minted by the CLI; every reply repeats it
	Verb   string `json:"verb"`             // run, stop, status, abandon, replay, plan or clean
	Target string `json:"target,omitempty"` // an epic label, a group label or a run id
	Root   string `json:"root,omitempty"`   // the repository root the command ran in; status covers this repository unless Flags.All is set
	Flags  Flags  `json:"flags"`
	Event  *Event `json:"event,omitempty"` // the run-start, run-stop, run-continue or plan event the CLI publishes
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
	ID     string          `json:"id"`               // the request's id
	Status string          `json:"status"`           // ok, refused, error or event
	Code   string          `json:"code,omitempty"`   // an error value from the code constants
	Detail string          `json:"detail,omitempty"` // one sentence for a person
	Data   json.RawMessage `json:"data,omitempty"`   // the verb's answer, such as a status table or a blocked group's question
	Event  *Event          `json:"event,omitempty"`  // set when Status is event
	Final  bool            `json:"final"`            // the last frame for this request
}
