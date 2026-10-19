package bus

import "time"

// RunStart is the data of run-start: a request to the Runtime Manager to run a target.
type RunStart struct {
	// the label the command named, such as ep-1 or tg-1.2; "all" for run --all; the epic label when the Intake Handler
	// starts the run
	Target string `json:"target"`
	// the group labels to run, such as tg-1.2
	Groups []string `json:"groups"`
	// "cli" or "intake": who published the request
	Source string `json:"source"`
	// RFC 3339 start time of a scheduled run; absent otherwise
	Schedule time.Time `json:"schedule,omitzero"`
	// the repository id the run belongs to
	Repo string `json:"repo"`
	// the repository root on this machine
	Root string `json:"root"`
	// the repository's git common directory, which holds its Komodo files
	CommonDir string `json:"common_dir"`
	// the control socket Request.ID of the command
	Request ID `json:"request,omitempty"`
}

// EventKey binds RunStart to run-start.
func (RunStart) EventKey() Key { return Key{TypeRunStart, ""} }

// RunStop is the data of run-stop: a request to stop a run, or to remove a queued scheduled run.
type RunStop struct {
	// an epic or group label, or a run id when one attempt is meant; a label means the latest attempt
	Target string `json:"target"`
	// the control socket Request.ID of the command
	Request ID `json:"request,omitempty"`
}

// EventKey binds RunStop to run-stop.
func (RunStop) EventKey() Key { return Key{TypeRunStop, ""} }

// RunContinue is the data of run-continue: a request for a blocked or paused group to go on.
type RunContinue struct {
	Target  string `json:"target"`            // an epic or group label
	Answer  string `json:"answer,omitempty"`  // "approve" or "deny" from --approve or --deny; empty for a plain continue
	Request ID     `json:"request,omitempty"` // the control socket Request.ID of the command
}

// EventKey binds RunContinue to run-continue.
func (RunContinue) EventKey() Key { return Key{TypeRunContinue, ""} }

// GroupFacts carries the stage, session and reason of a group event; embedded in a group fact's data.
type GroupFacts struct {
	Stage   string `json:"stage,omitempty"`   // the group's stage when the fact was seen: a state.Stage value
	Session ID     `json:"session,omitempty"` // session and task facts only
	Reason  string `json:"reason,omitempty"`  // a state.Reason value, block or exit
}

// RunStartedFact is the data of run-status / run.start.
type RunStartedFact struct {
	Groups     []ID   `json:"groups"`      // the groups the run queued, in queue order
	ConfigHash string `json:"config_hash"` // the effective configuration the run pinned
	Version    string `json:"version"`     // the binary version the run is pinned to
}

// EventKey binds RunStartedFact to run-status / run.start.
func (RunStartedFact) EventKey() Key { return Key{TypeRunStatus, "run.start"} }

// RunDone is the data of run-complete / done.
type RunDone struct {
	Done    []ID `json:"done"`    // groups that ended Done
	Blocked []ID `json:"blocked"` // groups left Blocked
}

// EventKey binds RunDone to run-complete / done.
func (RunDone) EventKey() Key { return Key{TypeRunComplete, "done"} }

// RunStopped is the data of run-complete / stopped.
type RunStopped struct {
	Signal string `json:"signal,omitempty"` // the signal received, such as SIGINT; empty for a stop command
	Saved  []ID   `json:"saved"`            // groups whose state the stop sequence saved
}

// EventKey binds RunStopped to run-complete / stopped.
func (RunStopped) EventKey() Key { return Key{TypeRunComplete, "stopped"} }

// GroupPaused is the data of run-status / group.paused.
type GroupPaused struct {
	GroupFacts
	Until time.Time `json:"until"` // the provider's reset time
	Link  string    `json:"link"`  // the link whose limit paused the group
}

// EventKey binds GroupPaused to run-status / group.paused.
func (GroupPaused) EventKey() Key { return Key{TypeRunStatus, "group.paused"} }

// GroupResumed is the data of run-status / group.resumed: a group left Paused or Blocked.
type GroupResumed struct {
	From string `json:"from"` // the stage the group was held in
	To   string `json:"to"`   // the stage it re-enters
}

// EventKey binds GroupResumed to run-status / group.resumed.
func (GroupResumed) EventKey() Key { return Key{TypeRunStatus, "group.resumed"} }

// GroupDone is the data of run-status / group.done: how a group ended.
type GroupDone struct {
	Outcome string `json:"outcome"` // complete, abandoned or lost
}

// EventKey binds GroupDone to run-status / group.done.
func (GroupDone) EventKey() Key { return Key{TypeRunStatus, "group.done"} }

// GroupApproved is the data of run-status / group.approved: the Tester and the Reviewer approved a group's work.
type GroupApproved struct {
	Tree string `json:"tree"` // the tree hash both judges approved, which the Commit Handler publishes
}

// EventKey binds GroupApproved to run-status / group.approved.
func (GroupApproved) EventKey() Key { return Key{TypeRunStatus, "group.approved"} }

// GroupBlocked is the data of run-failure / group.blocked; the reason is the block reason.
type GroupBlocked struct {
	GroupFacts
	Prior   string   `json:"prior,omitempty"`   // the working stage the group left
	Paths   []string `json:"paths,omitempty"`   // needs_scope only: the paths asked for
	Counter string   `json:"counter,omitempty"` // unnamed block only: retries, returns or flap
	Detail  string   `json:"detail,omitempty"`  // what the CLI shows, at most 4 KB
}

// EventKey binds GroupBlocked to run-failure / group.blocked.
func (GroupBlocked) EventKey() Key { return Key{TypeRunFailure, "group.blocked"} }

// LeaseLost is the data of run-failure / lease.lost.
type LeaseLost struct {
	GroupFacts
	Fence  uint64 `json:"fence"`            // the higher fence number found
	Holder ID     `json:"holder,omitempty"` // the run now holding the lease, when the lease names it
	Where  string `json:"where"`            // "renew" for a refused renewal, "push" for the ownership check before a push
}

// EventKey binds LeaseLost to run-failure / lease.lost.
func (LeaseLost) EventKey() Key { return Key{TypeRunFailure, "lease.lost"} }

// StageEnter is the data of run-status / stage.enter.
type StageEnter struct {
	From   string `json:"from"`   // the stage left; empty for the first entry
	To     string `json:"to"`     // the stage entered
	Return bool   `json:"return"` // a failure sent the group back to Build
}

// EventKey binds StageEnter to run-status / stage.enter.
func (StageEnter) EventKey() Key { return Key{TypeRunStatus, "stage.enter"} }

// StageExit is the data of run-status / stage.exit.
type StageExit struct {
	From string `json:"from"` // the stage whose gate was met
}

// EventKey binds StageExit to run-status / stage.exit.
func (StageExit) EventKey() Key { return Key{TypeRunStatus, "stage.exit"} }

// SessionStart is the data of run-status / session.start.
type SessionStart struct {
	GroupFacts
	Agent   string `json:"agent"`          // builder, tester, reviewer or planner
	Task    string `json:"task,omitempty"` // builder only: the task claimed with it
	Link    string `json:"link"`           // the model link: claude-cli or anthropic-api
	Model   string `json:"model"`          // the model identifier the link reported
	Resumed bool   `json:"resumed"`        // continued from a saved handle, so it adds no attempt
}

// EventKey binds SessionStart to run-status / session.start.
func (SessionStart) EventKey() Key { return Key{TypeRunStatus, "session.start"} }

// SessionIdle is the data of run-status / session.idle.
type SessionIdle struct {
	GroupFacts
	IdleS  int64 `json:"idle_s"` // seconds with no stream line and no running tool
	Nudged bool  `json:"nudged"` // true when the one nudge was sent; false when the stop ladder starts
}

// EventKey binds SessionIdle to run-status / session.idle.
func (SessionIdle) EventKey() Key { return Key{TypeRunStatus, "session.idle"} }

// SessionTimewarn is the data of run-status / session.timewarn.
type SessionTimewarn struct {
	GroupFacts
	UsedS int64 `json:"used_s"` // wall seconds used
	WallS int64 `json:"wall_s"` // the session's wall limit in seconds
}

// EventKey binds SessionTimewarn to run-status / session.timewarn.
func (SessionTimewarn) EventKey() Key { return Key{TypeRunStatus, "session.timewarn"} }

// SessionEnd is the data of run-status / session.end; the reason is the exit reason.
type SessionEnd struct {
	GroupFacts
	Agent       string         `json:"agent"`
	Task        string         `json:"task,omitempty"` // builder only: the task held at the end
	Link        string         `json:"link"`
	Handle      string         `json:"handle,omitempty"`       // the provider's resume handle, kept for paused and provider
	ResetAt     time.Time      `json:"reset_at,omitzero"`      // paused only: the provider's reset time
	Tokens      int64          `json:"tokens"`                 // input plus output tokens
	Usage       *SessionUsage  `json:"usage,omitempty"`        // token and cost detail the model client reported
	WallS       int64          `json:"wall_s"`                 // seconds the session ran
	ScopePaths  []string       `json:"scope_paths,omitempty"`  // needs_scope only: the paths asked for
	ScopeReason string         `json:"scope_reason,omitempty"` // needs_scope only: the builder's reason
	Verify      *VerifyOutcome `json:"verify,omitempty"`       // reviewer or tester with reason done
}

// EventKey binds SessionEnd to run-status / session.end.
func (SessionEnd) EventKey() Key { return Key{TypeRunStatus, "session.end"} }

// SessionUsage is the token and cost part of a session, as the model client reported it.
type SessionUsage struct {
	TokensIn    int     `json:"tokens_in"`
	TokensOut   int     `json:"tokens_out"`
	CacheRead   int     `json:"cache_read"`
	CacheWrite  int     `json:"cache_write"`
	USD         float64 `json:"usd"`          // metered cost; zero on a subscription link
	USDEquiv    float64 `json:"usd_equiv"`    // notional cost of a subscription session
	SkillTokens int     `json:"skill_tokens"` // tokens of the skills in the brief
}

// VerifyOutcome is a validated reviewer or tester result.
type VerifyOutcome struct {
	Pass            bool     `json:"pass"`                       // every task met or confirmed and every item passed
	NotMet          []string `json:"not_met,omitempty"`          // task identifiers marked not_met or not_confirmed
	BriefIncomplete bool     `json:"brief_incomplete,omitempty"` // the reviewer answered brief_incomplete
	Failed          []string `json:"failed,omitempty"`           // tester items the reruns confirmed failed
	Disputed        []string `json:"disputed,omitempty"`         // tester items the reruns passed
	Split           []string `json:"split,omitempty"`            // tester items whose two reruns disagreed
}

// TaskClaimed is the data of run-status / task.claimed.
type TaskClaimed struct {
	GroupFacts
	Task string `json:"task"` // the task identifier
}

// EventKey binds TaskClaimed to run-status / task.claimed.
func (TaskClaimed) EventKey() Key { return Key{TypeRunStatus, "task.claimed"} }

// TaskReleased is the data of run-status / task.released.
type TaskReleased struct {
	GroupFacts
	Task string `json:"task"`
}

// EventKey binds TaskReleased to run-status / task.released.
func (TaskReleased) EventKey() Key { return Key{TypeRunStatus, "task.released"} }

// TaskDone is the data of run-status / task.done.
type TaskDone struct {
	GroupFacts
	Task string `json:"task"`
}

// EventKey binds TaskDone to run-status / task.done.
func (TaskDone) EventKey() Key { return Key{TypeRunStatus, "task.done"} }

// TaskRetry is the data of run-status / task.retry; the reason is the cause.
type TaskRetry struct {
	GroupFacts
	Task    string `json:"task"`
	Attempt int    `json:"attempt"` // the attempt the fresh session will be
}

// EventKey binds TaskRetry to run-status / task.retry.
func (TaskRetry) EventKey() Key { return Key{TypeRunStatus, "task.retry"} }

// FindingSubmit is the data of run-status / finding.submit.
type FindingSubmit struct {
	GroupFacts
	Rule  string `json:"rule"`  // a rule identifier from a lens catalog
	File  string `json:"file"`  // path from the worktree root
	Line  int    `json:"line"`  // line number from 1 in the head tree
	Quote string `json:"quote"` // the quoted evidence line
}

// EventKey binds FindingSubmit to run-status / finding.submit.
func (FindingSubmit) EventKey() Key { return Key{TypeRunStatus, "finding.submit"} }

// FindingVerified is the data of run-status / finding.verified.
type FindingVerified struct {
	FindingSubmit
	Severity string `json:"severity"` // blocker, major or minor, from the rule
	In       string `json:"in"`       // "head" or "diff", where the quote was found
}

// EventKey binds FindingVerified to run-status / finding.verified.
func (FindingVerified) EventKey() Key { return Key{TypeRunStatus, "finding.verified"} }

// ToolDenied is the data of run-status / tool.denied.
type ToolDenied struct {
	GroupFacts
	Tool   string `json:"tool"`             // the tool refused, such as edit
	Row    string `json:"row"`              // the guard row, such as B1
	Target string `json:"target,omitempty"` // the path or command refused, secrets masked
	Count  int    `json:"count"`            // refusals under this row in this session
}

// EventKey binds ToolDenied to run-status / tool.denied.
func (ToolDenied) EventKey() Key { return Key{TypeRunStatus, "tool.denied"} }

// SandboxFallback is the data of run-status / sandbox.fallback.
type SandboxFallback struct {
	GroupFacts
	Role   string `json:"role"`   // the agent whose commands moved, such as tester
	Wanted string `json:"wanted"` // the mechanism the role should have used, such as docker
	Used   string `json:"used"`   // the mechanism it ran in, such as seatbelt or bubblewrap
	Why    string `json:"why"`    // why the wanted mechanism was not used
}

// EventKey binds SandboxFallback to run-status / sandbox.fallback.
func (SandboxFallback) EventKey() Key { return Key{TypeRunStatus, "sandbox.fallback"} }

// ToolOutside is the data of run-status / tool.outside.
type ToolOutside struct {
	GroupFacts
	Server  string `json:"server"`  // the outside tool server's name
	Tool    string `json:"tool"`    // the tool called
	Outcome string `json:"outcome"` // ok, error or timeout
}

// EventKey binds ToolOutside to run-status / tool.outside.
func (ToolOutside) EventKey() Key { return Key{TypeRunStatus, "tool.outside"} }

// EpicPaused is the data of run-status / epic.paused.
type EpicPaused struct {
	Epic   string `json:"epic"`   // the epic label, such as ep-1
	Group  string `json:"group"`  // the label of the group whose landing paused the epic
	Output string `json:"output"` // the exact output of the failing recheck
}

// EventKey binds EpicPaused to run-status / epic.paused.
func (EpicPaused) EventKey() Key { return Key{TypeRunStatus, "epic.paused"} }

// EpicResumed is the data of run-status / epic.resumed.
type EpicResumed struct {
	Epic  string `json:"epic"`  // the epic label
	Group string `json:"group"` // the label of the group whose recheck passed
}

// EventKey binds EpicResumed to run-status / epic.resumed.
func (EpicResumed) EventKey() Key { return Key{TypeRunStatus, "epic.resumed"} }

// TimerDue is the data of run-status / timer.due; reason pause_timeout marks the pause cap.
type TimerDue struct {
	GroupFacts
	Until time.Time `json:"until"` // the wait time, or the cap time, that passed
}

// EventKey binds TimerDue to run-status / timer.due.
func (TimerDue) EventKey() Key { return Key{TypeRunStatus, "timer.due"} }

// CheckDone is the data of run-status / check.done: the result of the free mechanical checks.
type CheckDone struct {
	GroupFacts
	Pass       bool        `json:"pass"`       // every step passed or warned
	Tip        bool        `json:"tip"`        // the checks ran on the epic branch after a landing
	Head       string      `json:"head"`       // the commit checked
	Regression bool        `json:"regression"` // a test that passed at the base fails
	Steps      []CheckStep `json:"steps"`
}

// EventKey binds CheckDone to run-status / check.done.
func (CheckDone) EventKey() Key { return Key{TypeRunStatus, "check.done"} }

// CheckStep is one check step's result.
type CheckStep struct {
	Step   string   `json:"step"`             // a step name, such as coverage
	Level  string   `json:"level"`            // pass, warn or fail
	Detail []string `json:"detail,omitempty"` // the failing tests, files or secret locations
}

// ProviderSkip is the data of run-status / provider.skip.
type ProviderSkip struct {
	GroupFacts
	From string `json:"from"` // the link skipped
	To   string `json:"to"`   // the next link tried; empty when none is left
}

// EventKey binds ProviderSkip to run-status / provider.skip.
func (ProviderSkip) EventKey() Key { return Key{TypeRunStatus, "provider.skip"} }

// LockTaken is the data of run-status / lock.taken.
type LockTaken struct {
	CardHash string   `json:"card_hash"` // the hash of the group's task-list files
	Tasks    []string `json:"tasks"`     // the locked task identifiers, in file-name order
}

// EventKey binds LockTaken to run-status / lock.taken.
func (LockTaken) EventKey() Key { return Key{TypeRunStatus, "lock.taken"} }

// LockReleased is the data of run-status / lock.released.
type LockReleased struct {
	By string `json:"by"` // what released the lock
}

// EventKey binds LockReleased to run-status / lock.released.
func (LockReleased) EventKey() Key { return Key{TypeRunStatus, "lock.released"} }

// LeaseTaken is the data of run-status / lease.taken.
type LeaseTaken struct {
	Fence uint64 `json:"fence"` // the fence number the claim wrote
	PID   int    `json:"pid"`   // the holder's process identifier
}

// EventKey binds LeaseTaken to run-status / lease.taken.
func (LeaseTaken) EventKey() Key { return Key{TypeRunStatus, "lease.taken"} }

// LimitWarn is the data of resource / limit.warn.
type LimitWarn struct {
	Link string  `json:"link"`
	Kind string  `json:"kind"`          // quota, overage or spend
	Pct  int     `json:"pct,omitempty"` // quota only: percent of the window used
	USD  float64 `json:"usd,omitempty"` // spend only: dollars spent in the run
}

// EventKey binds LimitWarn to resource / limit.warn.
func (LimitWarn) EventKey() Key { return Key{TypeResource, "limit.warn"} }

// --- Bus

// BusOverflow is the data of error / bus.overflow, published when a subscription's queue is full.
type BusOverflow struct {
	Subscription string `json:"subscription"` // the name given to Subscribe
	Dropped      uint64 `json:"dropped"`      // events lost so far
}

// EventKey binds BusOverflow to error / bus.overflow.
func (BusOverflow) EventKey() Key { return Key{TypeError, "bus.overflow"} }

// BusStoreFailed is the data of error / bus.store_failed, published once per burst of failed appends to the event store.
type BusStoreFailed struct {
	Run    ID     `json:"run"`    // the run whose event was not stored; empty for an event with no run
	Path   string `json:"path"`   // the file the append went to
	Reason string `json:"reason"` // why the append failed
}

// EventKey binds BusStoreFailed to error / bus.store_failed.
func (BusStoreFailed) EventKey() Key { return Key{TypeError, "bus.store_failed"} }

// RequestRef names the command that a request outcome answers.
type RequestRef struct {
	// the control socket Request.ID of the command, copied into the command event's data as request
	Request ID `json:"request"`
}

// RequestAccepted is the data of run-status / request.accepted.
type RequestAccepted struct {
	RequestRef
}

// EventKey binds RequestAccepted to run-status / request.accepted.
func (RequestAccepted) EventKey() Key { return Key{TypeRunStatus, "request.accepted"} }

// RequestRefused is the data of run-status / request.refused.
type RequestRefused struct {
	RequestRef
	Why string `json:"why"` // what the CLI shows, such as "group is running"
}

// EventKey binds RequestRefused to run-status / request.refused.
func (RequestRefused) EventKey() Key { return Key{TypeRunStatus, "request.refused"} }
