// Package task is the Task Manager: it guards every agent action, runs the agents' tools and carries the calls between one
// task group and the Runtime Manager.
package task

import (
	"context"
	"encoding/json"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// GroupSpec is everything one Task Manager needs to run its group.
type GroupSpec struct {
	Run        string                 // run identifier, a version 7 UUID
	Group      string                 // task group label, such as tg-1.2
	Tasks      []TaskSpec             // tasks in order
	Files      []string               // the group's file globs; the builder writes only here
	Lease      LeaseRef               // branch, worktree and git folder the lease records
	Agents     map[string]Permissions // permissions per agent role
	Servers    []OutsideServer        // outside tool servers switched on for at least one agent
	Limits     Limits                 // session clocks, returns, refusals, attempts
	Saved      *Progress              // the group's saved state when `run` continues it; nil for a new group
	OtherFiles []string               // file globs of the other groups in the run
}

// TaskSpec is one task of the group.
type TaskSpec struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	DoneWhen string `json:"done_when"` // command class the builder's task_done reruns
}

// LeaseRef is the lease's record for one group.
type LeaseRef struct {
	Branch   string `json:"branch"`
	Worktree string `json:"worktree"` // absolute path
	GitDir   string `json:"git_dir"`  // this worktree's own git folder, absolute path
	Fence    int64  `json:"fence"`    // rising number that proves the lease is current
}

// Permissions is what one agent may do; an empty value allows nothing.
type Permissions struct {
	Tools      []string `json:"tools"`       // the only tools advertised and callable
	RunClasses []string `json:"run_classes"` // the only command classes `run` may start
	Guard      []RowID  `json:"guard"`       // guard rows beyond the ones every agent inherits
	Servers    []string `json:"servers"`     // outside tool servers switched on for this agent
	Weight     string   `json:"weight"`      // heavy, standard or light
	Chain      []string `json:"chain"`       // link names in order
}

// Limits are the numbers the Task Manager enforces, resolved by the Runtime Manager from settings.
type Limits struct {
	SessionWall  time.Duration
	SessionIdle  time.Duration
	GroupRetries int // the group's retry budget at its start: failures it may still retry
	RefusalLimit int // refusals under one guard row before the session ends
}

// OutsideServer is one outside tool server from the Context Manager's list, switched on for at least one agent.
type OutsideServer struct {
	Name    string   `json:"name"`
	Command string   `json:"command"` // started without a shell
	Args    []string `json:"args,omitempty"`
	Env     []string `json:"env,omitempty"`   // variable names only; the values come from the harness environment
	Hosts   []string `json:"hosts,omitempty"` // hosts the server may reach; empty means no network
}

// Progress is the Task Manager's own counters; the Runtime Manager persists them through ReportState.
type Progress struct {
	Stage     state.Stage `json:"stage"`
	TasksDone []string    `json:"tasks_done"`
	Returns   int         `json:"returns"` // trips back to Build so far, for the brief; not a limit
	// tester restarts after a disputed result
	Disputes int `json:"disputes"`
	// retries the group may still spend; mirrors the engine's budget and only falls
	RetriesLeft int `json:"retries_left"`
	// tree hash when the group last went back to Build
	TreeAtReturn string `json:"tree_at_return"`
	// task identifiers that already made their one scope request
	ScopeAsked []string `json:"scope_asked"`
	// paths a scope answer added to the group's files
	Widened []string `json:"widened"`
	// refusals per guard row in the current session
	Refusals map[string]int `json:"refusals"`
	Pause    *PauseState    `json:"pause,omitempty"`
	// handle of each agent's live session, reported before the session starts
	Handles map[string]llm.ResumeHandle `json:"handles,omitempty"`
	// the task whose edits a builder session left uncommitted in the worktree
	Unfinished *state.UnfinishedEdits `json:"unfinished,omitempty"`
}

// PauseState is a group waiting out a provider limit.
type PauseState struct {
	Since          time.Time         `json:"since"`           // first pause of this wait; kept across re-pauses
	WaitUntil      time.Time         `json:"wait_until"`      // latest reset time among halted sessions, plus 1 minute
	WaitLink       string            `json:"wait_link"`       // the link whose reset the group waits for
	RefusedResumes int               `json:"refused_resumes"` // resumes the provider refused at 0 turns
	Owed           []string          `json:"owed"`            // agents to start at the timer, instead of starting now
	Handle         *llm.ResumeHandle `json:"handle,omitempty"`
}

// Escalation is a blocker or a request the Task Manager cannot settle alone.
type Escalation struct {
	ID     string          `json:"id"` // unique; an Answer names it
	Group  string          `json:"group"`
	Kind   EscalationKind  `json:"kind"`   // scope, blocked or failed
	Reason string          `json:"reason"` // needs_scope, returns, flap, attempts, refusals and the other reasons
	Detail json.RawMessage `json:"detail,omitempty"`
}

// EscalationKind says what the Task Manager needs back.
type EscalationKind string

// Answer is the one reply to an Escalation, delivered through Manager.Continue.
type Answer struct {
	Escalation string   `json:"escalation"`         // the Escalation.ID
	Action     string   `json:"action"`             // approve (komodo run --approve) or deny (--deny)
	Approved   []string `json:"approved,omitempty"` // scope: paths added to the group's files
}

// TimerRequest asks the Runtime Manager for more wall time for one session.
type TimerRequest struct {
	Session string        `json:"session"`
	Used    time.Duration `json:"used"`
	Limit   time.Duration `json:"limit"`
}

// TimerGrant is the Runtime Manager's reply; zero Extra means no more time.
type TimerGrant struct {
	Extra time.Duration `json:"extra"`
}

// StateReport is one update the Runtime Manager persists and publishes.
type StateReport struct {
	Group    string   `json:"group"`
	Progress Progress `json:"progress"`
	Facts    []Fact   `json:"facts,omitempty"` // things that happened since the last report
}

// Fact is one thing that happened; the Runtime Manager publishes it as an event.
type Fact struct {
	Subtype string          `json:"subtype"` // such as session.start or tool.denied
	At      time.Time       `json:"at"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Runtime is the Runtime Manager as the Task Manager sees it.
type Runtime interface {
	// Escalate hands over a blocker or a request; it returns once the Runtime Manager accepted it.
	Escalate(ctx context.Context, e Escalation) error
	// ExtendTimer asks for more wall time for one session and waits for the reply.
	ExtendTimer(ctx context.Context, r TimerRequest) (TimerGrant, error)
	// ReportState hands over the group's progress and the facts since the last report.
	ReportState(ctx context.Context, s StateReport) error
}

// The escalation kinds.
const (
	EscScope   EscalationKind = "scope"   // a builder asked for files outside its list; waits for an answer
	EscBlocked EscalationKind = "blocked" // work stopped; the Runtime Manager decides retry, hold or end
	EscFailed  EscalationKind = "failed"  // work cannot continue; the group ends
)
