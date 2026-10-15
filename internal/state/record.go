package state

import (
	"encoding/json"
	"time"
)

// Header is the five fields every record carries; the manager fills in all of them.
type Header struct {
	V      int       `json:"v"`      // schema version the record was written under
	Group  string    `json:"group"`  // task group id; the record's key
	Rev    int       `json:"rev"`    // rises by 1 on every write
	TS     time.Time `json:"ts"`     // when the manager wrote the record
	Writer string    `json:"writer"` // component, run and process of the writer
}

// Record is one stored group record as a reader receives it.
type Record struct {
	Header Header
	State  GroupState
}

// Stage is where a group stands in the pipeline.
type Stage string

const (
	StageIntake     Stage = "intake"      // the Intake Handler's planning stage
	StageBuild      Stage = "build"       // the Builder works through the tasks
	StageTestReview Stage = "test_review" // the Tester and Reviewer judge in parallel
	StageCommit     Stage = "commit"      // the Commit Handler publishes
	StageCleanup    Stage = "cleanup"     // the Cleanup Handler frees resources
	StageDone       Stage = "done"        // the group is closed
	StagePaused     Stage = "paused"      // a plan window or a spending ceiling is reached; the lease is kept and the group resumes when the window resets
	StageBlocked    Stage = "blocked"     // needs a person or the orchestrator
)

// GroupState is one task group's runtime state: the body of a group record.
type GroupState struct {
	Run         string           `json:"run"`                  // run that holds the group
	Group       string           `json:"group"`                // task group id
	Stage       Stage            `json:"stage"`                // current stage
	Prior       Stage            `json:"prior"`                // the working stage a paused or blocked group left
	Epoch       uint32           `json:"epoch"`                // rises each time the group enters a stage; an input from an older epoch is stale
	Outcome     string           `json:"outcome"`              // set in done: complete, lost or abandoned
	Fence       uint64           `json:"fence"`                // the Lease Manager's fence for this holder; never zero
	ConfigHash  string           `json:"config_hash"`          // SHA-256 of the settings pinned at run start
	Counters    Counters         `json:"counters"`             // numbers the Runtime Manager reads
	Tasks       []TaskState      `json:"tasks"`                // progress of each task
	TestReview  TestReview       `json:"test_review"`          // results of the current test and review round
	ActionsOpen []OpenAction     `json:"actions_open"`         // actions ordered but not yet done
	Resume      []ResumeRef      `json:"resume"`               // how each interrupted session can continue
	Pause       PauseState       `json:"pause"`                // set while the group is paused
	Block       BlockState       `json:"block"`                // set while the group is blocked
	FailureHash string           `json:"failure_hash"`         // fingerprint of the failure behind the last return to build
	ReturnTree  string           `json:"return_tree"`          // tree hash tested at the last return; the same tree again is a flap
	Owed        []string         `json:"owed"`                 // agents whose restart is owed until a pause ends
	Requests    []string         `json:"requests"`             // ids of requests applied, newest last, at most 64
	ScopeAdded  []string         `json:"scope_added"`          // file patterns the orchestrator approved for the group
	Unfinished  *UnfinishedEdits `json:"unfinished,omitempty"` // the task whose edits a Builder session left in the worktree
	LastEventAt time.Time        `json:"last_event_at"`        // time of the previous applied change, for wall time
}

// UnfinishedEdits names a task whose edits sit in the worktree uncommitted.
type UnfinishedEdits struct {
	Task  string   `json:"task"`  // the task identifier
	Files []string `json:"files"` // the changed files, paths from the worktree root
}

// Counters are the numbers that bound a group; only the Runtime Manager changes them.
type Counters struct {
	Returns      int            `json:"returns"`       // returns to build after a failed test or review
	Attempts     int            `json:"attempts"`      // fresh builder sessions for the group
	Retries      int            `json:"retries"`       // builder restarts after a timeout or idle stop
	Crashes      int            `json:"crashes"`       // sessions lost to a crash; never charged to attempts or retries
	Blocks       map[string]int `json:"blocks"`        // blocks per reason
	WallUsedS    int64          `json:"wall_used_s"`   // seconds worked, excluding paused and blocked time
	WallExtended bool           `json:"wall_extended"` // the one wall-time extension was granted
	TokensUsed   int64          `json:"tokens_used"`   // tokens used by the group
}

// TaskState is one task's progress.
type TaskState struct {
	ID           string `json:"id"`                // task id, such as tsk-add-lock
	State        string `json:"state"`             // queued, claimed or done
	Session      string `json:"session,omitempty"` // session working the task
	ScopeAsked   bool   `json:"scope_asked"`       // the task's one request for more files is spent
	GateFailures int    `json:"gate_failures"`     // failed completion checks for this task
}

// TestReview is the Tester's and Reviewer's progress in the current round.
type TestReview struct {
	Tester   RoundState `json:"tester"`
	Reviewer RoundState `json:"reviewer"`
	Findings []Finding  `json:"findings"` // blocking findings Go verified this round
	NotMet   []string   `json:"not_met"`  // task ids the Tester marked not met
	Failed   []string   `json:"failed"`   // tester items confirmed failed
}

// RoundState is one agent's progress in the current round.
type RoundState struct {
	Session  string `json:"session,omitempty"` // live or last session of the agent
	Result   string `json:"result"`            // empty until it ends, then pass, fail or cancelled
	Restarts int    `json:"restarts"`          // fresh sessions after an end without a result
}

// Finding names one verified blocking finding by rule and file.
type Finding struct {
	Rule string `json:"rule"`
	File string `json:"file"`
}

// OpenAction is a side effect the Runtime Manager ordered that has not finished.
type OpenAction struct {
	ID      string          `json:"id"`      // fingerprint of the order, so a rerun is recognised
	Kind    string          `json:"kind"`    // the kind of action
	Args    json.RawMessage `json:"args"`    // arguments of that kind
	Started bool            `json:"started"` // the action began; a rerun after a crash skips an action whose id is done
}

// ResumeRef is how an interrupted session can be continued.
type ResumeRef struct {
	Agent   string `json:"agent,omitempty"` // the agent whose session halted
	Session string `json:"session,omitempty"`
	Handle  string `json:"handle,omitempty"` // the provider's conversation handle
	Link    string `json:"link,omitempty"`   // the model link the session used
}

// PauseState holds a paused group's wait.
type PauseState struct {
	Until   time.Time `json:"until,omitzero"`  // when the limit resets, plus a margin
	Since   time.Time `json:"since,omitzero"`  // when the group paused
	Link    string    `json:"link,omitempty"`  // the limited link
	Refused int       `json:"refused"`         // resume attempts the provider refused, for the backoff
	Cause   string    `json:"cause,omitempty"` // provider, plan_window, spending or group_tokens
	After   bool      `json:"after,omitempty"` // a limit pause waits for the live session to end
}

// BlockState names why and when a blocked group stopped.
type BlockState struct {
	Reason  Reason    `json:"reason,omitempty"`
	Counter string    `json:"counter,omitempty"` // the spent counter of an unnamed block
	At      time.Time `json:"at,omitzero"`
}
