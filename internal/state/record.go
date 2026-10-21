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
	// the Intake Handler's planning stage
	StageIntake Stage = "intake"

	// the Builder works through the tasks
	StageBuild Stage = "build"

	// Quality Assurance and Reviewer judge in parallel
	StageTestReview Stage = "test_review"

	// the Commit Handler publishes
	StageCommit Stage = "commit"

	// the Cleanup Handler frees resources
	StageCleanup Stage = "cleanup"

	// the group is closed
	StageDone Stage = "done"

	// a plan window or a spending ceiling is reached; the lease is kept and the group resumes when the window resets
	StagePaused Stage = "paused"

	// needs a person or the orchestrator
	StageBlocked Stage = "blocked"
)

// GroupState is one task group's runtime state: the body of a group record.
type GroupState struct {
	// run that holds the group
	Run string `json:"run"`

	// task group id
	Group string `json:"group"`

	// current stage
	Stage Stage `json:"stage"`

	// the working stage a paused or blocked group left
	Prior Stage `json:"prior"`

	// rises each time the group enters a stage; an input from an older epoch is stale
	Epoch uint32 `json:"epoch"`

	// set in done: complete, lost, abandoned or failed
	Outcome string `json:"outcome"`

	// the Lease Manager's fence for this holder; never zero
	Fence uint64 `json:"fence"`

	// SHA-256 of the settings pinned at run start
	ConfigHash string `json:"config_hash"`

	// numbers the Runtime Manager reads
	Counters Counters `json:"counters"`

	// progress of each task
	Tasks []TaskState `json:"tasks"`

	// results of the current test and review round
	TestReview TestReview `json:"test_review"`

	// actions ordered but not yet done
	ActionsOpen []OpenAction `json:"actions_open"`

	// how each interrupted session can continue
	Resume []ResumeRef `json:"resume"`

	// set while the group is paused
	Pause PauseState `json:"pause"`

	// set while the group is blocked
	Block BlockState `json:"block"`

	// fingerprint of the failure behind the last return to build
	FailureHash string `json:"failure_hash"`

	// the output of the last failed test and review round, for the builder after an override
	LastFindings string `json:"last_findings,omitempty"`

	// tree hash tested at the last return; the same tree again is a flap
	ReturnTree string `json:"return_tree"`

	// agents whose restart is owed until a pause ends
	Owed []string `json:"owed"`

	// ids of requests applied, newest last, at most 64
	Requests []string `json:"requests"`

	// file patterns the orchestrator approved for the group
	ScopeAdded []string `json:"scope_added"`

	// the task whose edits a Builder session left in the worktree
	Unfinished *UnfinishedEdits `json:"unfinished,omitempty"`

	// time of the previous applied change, for wall time
	LastEventAt time.Time `json:"last_event_at"`
}

// UnfinishedEdits names a task whose edits sit in the worktree uncommitted.
type UnfinishedEdits struct {
	Task  string   `json:"task"`  // the task identifier
	Files []string `json:"files"` // the changed files, paths from the worktree root
}

// Counters are the numbers that bound a group; only the Runtime Manager changes them.
type Counters struct {
	RetriesLeft int            `json:"retries_left"` // tries left, the current one included; only an override raises it
	Overrides   int            `json:"overrides"`    // overrides a person approved; the only way retries_left rises
	Crashes     int            `json:"crashes"`      // sessions lost to a crash; never charged to the retries
	Blocks      map[string]int `json:"blocks"`       // blocks per reason
	WallUsedS   int64          `json:"wall_used_s"`  // seconds worked, excluding paused and blocked time; never falls
	TokensUsed  int64          `json:"tokens_used"`  // tokens used by the group
	Tries       int            `json:"tries"`        // the current try, from 1; rises with a spent retry or override
	WallBaseS   int64          `json:"wall_base_s"`  // wall_used_s when the current try began; only rises
	WallExtTry  int            `json:"wall_ext_try"` // the try the one wall-time extension was granted on; only rises
	FaultStreak int            `json:"fault_streak"` // Komodo faults in a row with no session result between them

	FinalWallMin int `json:"final_wall_min,omitempty"` // the final try's minutes; 0 until an override; only rises
}

// TaskState is one task's progress.
type TaskState struct {
	ID           string `json:"id"`                // task id, such as tsk-add-lock
	State        string `json:"state"`             // queued, claimed or done
	Session      string `json:"session,omitempty"` // session working the task
	ScopeAsked   bool   `json:"scope_asked"`       // the task's one request for more files is spent
	GateFailures int    `json:"gate_failures"`     // failed completion checks for this task
}

// TestReview is Quality Assurance's and Reviewer's progress in the current round.
type TestReview struct {
	QA       RoundState `json:"qa"`
	Reviewer RoundState `json:"reviewer"`
	Findings []Finding  `json:"findings"` // blocking findings Go verified this round
	NotMet   []string   `json:"not_met"`  // task ids Quality Assurance marked not met
	Failed   []string   `json:"failed"`   // Quality Assurance items marked failed
}

// RoundState is one agent's progress in the current round.
type RoundState struct {
	Session string `json:"session,omitempty"` // live or last session of the agent
	Result  string `json:"result"`            // empty until it ends, then pass, fail or cancelled
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
	Until   time.Time `json:"until,omitzero"` // retries only: when the override window lapses and the group fails
}
