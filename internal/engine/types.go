package engine

import (
	"encoding/json"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/lease"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// State is the body of the group record; the State Manager owns the one schema.
type State = state.GroupState

// Stage is a group's stage or holding state.
type Stage = state.Stage

// NoStage is the stage of a group that has no record yet.
const NoStage Stage = ""

// The two stage sentinels a transition row may name as its target.
const (
	Keep    Stage = "keep"  // stay in the current stage
	ToPrior Stage = "prior" // return to the stage the group left
)

// InputKind names a fact.
type InputKind string

// The input kinds.
const (
	InGroupStart    InputKind = "group.start"   // the group is ready and a pool slot is free
	InLeaseGranted  InputKind = "lease.granted" // data: LeaseGrant
	InReady         InputKind = "ready"         // the Task Manager is running and its sandbox is wired
	InStageDone     InputKind = "stage.done"    // data: StageDoneData
	InResultFail    InputKind = "result.fail"   // data: ResultFail
	InSessionStart  InputKind = "session.start" // data: SessionStartData
	InSessionEnd    InputKind = "session.end"   // data: SessionEndData
	InTaskDone      InputKind = "task.done"     // data: bus.TaskDone
	InGateFail      InputKind = "gate.fail"     // data: bus.TaskDone
	InScopeAsk      InputKind = "scope.ask"     // data: ScopeAsk
	InScopeAnswer   InputKind = "scope.answer"  // data: ScopeAnswer
	InTimerExtend   InputKind = "timer.extend"  // data: task.TimerRequest
	InTimerDue      InputKind = "timer.due"     // data: TimerDue
	InContinue      InputKind = "continue"      // data: ContinueData
	InLeaseLost     InputKind = "lease.lost"
	InFault         InputKind = "fault"          // data: FaultData
	InGroupClosed   InputKind = "group.closed"   // the Task Manager finished CloseTaskGroup
	InGroupCrash    InputKind = "group.crash"    // the panic-catching helper
	InLimitPause    InputKind = "limit.pause"    // data: LimitPause
	InAbandon       InputKind = "abandon"        // data: AbandonData
	InProgress      InputKind = "progress"       // data: ProgressData; the live session handles of a state report
	InFact          InputKind = "fact"           // data: FactData; a fact that is published and changes nothing
	InLoopRestart   InputKind = "loop.restart"   // a silent Task Manager was restarted; costs no retry
	InOverride      InputKind = "override"       // data: OverrideData; a person gave an empty group a final try
	InHoldDue InputKind = "hold.due" // a hold on the group's session starts ended; it performs the ones held back
)

// Input is one fact for one group; the Runtime Manager makes it from a task.Message, a command, a timer, a lease call or a
// PauseGroups call.
type Input struct {
	Kind  InputKind `json:"kind"`
	Group string    `json:"group"`
	// the epoch the sender started under; 0 for inputs the Runtime Manager makes
	Epoch uint32    `json:"epoch"`
	Time  time.Time `json:"time"`
	// identifier for commands, for once-only handling
	Request string          `json:"request,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// OrderKind names an instruction.
type OrderKind string

// The order kinds; the last three are performed by the Runtime Manager itself and never reach a Task Manager.
const (
	OrderStartGroup OrderKind = "start_group" // create the Task Manager
	OrderEnterStage OrderKind = "enter_stage" // run a stage, resuming the listed sessions
	OrderCancel     OrderKind = "cancel"      // stop every live session of the group
	OrderExtend     OrderKind = "extend"      // answer to timer.extend
	OrderScope      OrderKind = "scope"       // answer to scope.ask: approved or denied
	OrderHold       OrderKind = "hold"        // stop starting sessions; the live session may finish
	OrderGrantLease OrderKind = "grant_lease" // take the group's lease
	OrderRelease    OrderKind = "release"     // return the pool slot and release the lease
	OrderDiscard    OrderKind = "discard"     // free the group's branch, worktree and sandbox
)

// Order is one instruction from the group loop to its Task Manager, sent as a task.Command.
type Order struct {
	Kind   OrderKind         `json:"kind"`
	Key    string            `json:"key"` // idempotency key: group, kind and epoch
	Re     string            `json:"re,omitempty"`
	Stage  state.Stage       `json:"stage,omitempty"`
	Resume []state.ResumeRef `json:"resume,omitempty"`
	// the Context Manager's answer for the stage, by agent
	Context json.RawMessage `json:"context,omitempty"`
	// findings, scope answer, extension, wake reason
	Data json.RawMessage `json:"data,omitempty"`
}

// Result is everything one input decides.
type Result struct {
	State   State
	Orders  []Order  // performed by the group loop, each wrapped in start and done records
	Emit    []Event  // events to publish after the write is saved
	Refused bool     // no row matched; State is unchanged
	Why     string   // the sentence for a person when Refused, from the request.refused fact
	Stale   bool     // the input's epoch is older than the group's; State is unchanged
	Dup     bool     // the input's request was applied before; State is unchanged
	Dropped []string // facts that stood for no event, each with the kind and the reason, for the error output
	// the error type the input stood for, which the error handler records; empty for none
	Err state.Reason
}

// The data of each input kind.
type (
	// LeaseGrant is the data of lease.granted.
	LeaseGrant struct {
		Fence    uint64 `json:"fence"`
		Branch   string `json:"branch"`
		Worktree string `json:"worktree"`
	}
	// StageDoneData is the data of stage.done.
	StageDoneData struct {
		Stage state.Stage `json:"stage"`
	}
	// ResultFail is the data of result.fail.
	ResultFail struct {
		Items      []string `json:"items"`                // the failing items, as findings and test names
		Tree       string   `json:"tree,omitempty"`       // the tree hash that was tested
		Regression bool     `json:"regression,omitempty"` // a test that passed at the base fails
		Findings   string   `json:"findings,omitempty"`   // the exact output the builder receives
	}
	// SessionStartData is the data of session.start.
	SessionStartData struct {
		bus.SessionStart
		Restart bool `json:"restart,omitempty"` // a restart after a crash, which adds no attempt
	}
	// SessionEndData is the data of session.end.
	SessionEndData struct {
		bus.SessionEnd
		Unfinished *state.UnfinishedEdits `json:"unfinished,omitempty"`
		Restart    bool                   `json:"restart,omitempty"` // the engine ended the session by dying; no fault
	}
	// ScopeAsk is the data of scope.ask; Deny is set by the Runtime Manager when a path is protected or in another live
	// group's files.
	ScopeAsk struct {
		Task   string   `json:"task"`
		Paths  []string `json:"paths"`
		Reason string   `json:"reason"`
		Deny   string   `json:"deny,omitempty"`
		// Escalation names the Task Manager's escalation, which the answer replies to.
		Escalation string `json:"escalation,omitempty"`
	}
	// ScopeAnswer is the data of scope.answer.
	ScopeAnswer struct {
		Answer string   `json:"answer"`
		Paths  []string `json:"paths,omitempty"`
		Reason string   `json:"reason,omitempty"`
	}
	// TimerDue is the data of timer.due; Reason is timer, pause_timeout or run-continue.
	TimerDue struct {
		Reason string `json:"reason"`
	}
	// OverrideData is the data of override.
	OverrideData struct {
		Minutes int `json:"minutes,omitempty"` // the final try's working time; 0 keeps limits.group_wall_min
	}
	// ContinueData is the data of continue.
	ContinueData struct {
		Answer   string `json:"answer,omitempty"`   // approve or deny
		Recovery bool   `json:"recovery,omitempty"` // made by the recovery pass
	}
	// FaultData is the data of fault.
	FaultData struct {
		Reason string `json:"reason"`
		Detail string `json:"detail,omitempty"`
	}
	// LimitPause is the data of limit.pause; Until is zero when the pause has no end.
	LimitPause struct {
		Link  string    `json:"link,omitempty"`
		Cause string    `json:"cause"`
		Until time.Time `json:"until,omitzero"`
	}
	// AbandonData names the group that was abandoned; empty or the group itself means this group.
	AbandonData struct {
		Group string `json:"group,omitempty"`
	}
	// ProgressData is the data of progress: the resume reference of each live session.
	ProgressData struct {
		Live []state.ResumeRef `json:"live"`
	}
	// FactData is the data of fact: an event the Task Manager reported.
	FactData struct {
		Subtype string          `json:"subtype"`
		Data    json.RawMessage `json:"data,omitempty"`
	}
)

// GroupSpec is what the Runtime Manager reads from one task group's list.
type GroupSpec struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"` // the readable label, such as tg-1.2
	Epic     string   `json:"epic"`
	Depends  []string `json:"depends"`  // group identifiers that must be Done first
	Files    []string `json:"files"`    // the globs the group may touch
	Tasks    []string `json:"tasks"`    // task identifiers in order
	Priority string   `json:"priority"` // "high" or "normal"
	Order    int      `json:"order"`    // position in task-list order; lower is earlier

	// The Runtime Manager fills these before it starts the group's Task Manager.
	// the run that holds the group
	Run string `json:"-"`
	// the repository id
	Repo string `json:"-"`
	// the repository root on this machine
	Root string `json:"-"`
	// branch, worktree and fence
	Lease lease.Lease `json:"-"`
	// the bounds copied at run start
	Limits Limits `json:"-"`
	// the group's saved state when it continues; nil for a new group
	Saved *State `json:"-"`
	// file globs of the other groups in the run
	OtherFiles []string `json:"-"`
}

// Candidate is a ready group with the three facts the tie-break rules read.
type Candidate struct {
	Spec        GroupSpec
	ChainWeight int // the sum of task counts along the heaviest path of unfinished groups starting here
	Unblocks    int // groups whose only unfinished dependency is this group
}

// LandingPlan is what the Runtime Manager hands the Commit Handler for one epic.
type LandingPlan struct {
	Epic   string   `json:"epic"`   // the epic label, such as ep-1
	Branch string   `json:"branch"` // the epic's shared branch, komodo/ep-1
	Order  []string `json:"order"`  // the epic's group labels in the run, every group after its dependencies
}

// Limits are the bounds Next enforces, read once at run start through the Configuration Manager.
type Limits struct {
	GroupRetries int // limits.group_retries
	PauseMaxMin  int // limits.pause_max_min
	GroupWallMin int // limits.group_wall_min
	GroupTokens  int // limits.group_tokens, enforced by the Resource Manager
	PoolMin      int // limits.pool_min
	PoolMax      int // limits.pool_max
	SessionMemGB float64
}

// Timings and bounds of the engine.
const (
	OverrideWindow       = 60 * time.Minute // how long a group with no tries left waits for a person's override
	FinalWallMaxMin      = 480              // the most minutes a person may give the final try
	GroupBuffer          = 256              // capacity of a group's input channel
	LeaseRenewEvery      = 5 * time.Minute  // how often a live group renews its lease
	AgentStopGrace       = 30 * time.Second // time a stop allows each session before the terminate signal
	ShutdownDeadline     = 90 * time.Second // from the stop's start to the whole-tree kill
	PoolHalveCooldown    = 5 * time.Minute  // after a halving, further signals are ignored for this long
	BlocksBeforeRedesign = 3                // blocks for one reason before a group is flagged needs redesign
	EngineStartWait      = 5 * time.Second  // how long the CLI retries the socket after starting the engine
	CommandBuffer        = 16               // capacity of a Task Manager's command channel and reply channel
	HeartbeatEvery       = 5 * time.Second  // how often each core loop beats to the Runtime Manager
	LoopSilentFor        = 60 * time.Second // silence after which a core loop is stopped and restarted
	EngineBeatEvery      = 5 * time.Second  // how often the engine rewrites engine.beat
	HungBeatAge          = 30 * time.Second // beat file age after which the CLI treats an unanswering engine as hung
	HungReplyWait        = 10 * time.Second // how long the CLI waits for a socket reply before it checks the beat file
)

// pipelineStages lists the working stages in the order a group passes through them.
var pipelineStages = []Stage{StageIntake, StageBuild, StageTestReview, StageCommit, StageCleanup}

// SocketName is the control socket's file name inside the state folder.
const SocketName = "engine.sock"

// BeatName is the engine heartbeat file's name inside the state folder.
const BeatName = "engine.beat"

// The stages and holding states, under the names this package uses.
const (
	StageIntake     = state.StageIntake
	StageBuild      = state.StageBuild
	StageTestReview = state.StageTestReview
	StageCommit     = state.StageCommit
	StageCleanup    = state.StageCleanup
	StageDone       = state.StageDone
	StagePaused     = state.StagePaused
	StageBlocked    = state.StageBlocked
)
