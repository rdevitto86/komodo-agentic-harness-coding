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
	Answer  string `json:"answer,omitempty"`  // "approve", "deny" or "override"; empty for a plain continue
	Minutes int    `json:"minutes,omitempty"` // override only: the final try's working time in minutes
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
	Outcome string `json:"outcome"` // complete, abandoned, lost or failed
}

// EventKey binds GroupDone to run-status / group.done.
func (GroupDone) EventKey() Key { return Key{TypeRunStatus, "group.done"} }

// GroupApproved is the data of run-status / group.approved: Quality Assurance and the Reviewer approved a group's work.
// It has no body; the commit in git is the record of the code.
type GroupApproved struct{}

// EventKey binds GroupApproved to run-status / group.approved.
func (GroupApproved) EventKey() Key { return Key{TypeRunStatus, "group.approved"} }

// GroupBlocked is the data of run-failure / group.blocked; the reason is the block reason.
type GroupBlocked struct {
	GroupFacts
	Prior   string    `json:"prior,omitempty"`   // the working stage the group left
	Paths   []string  `json:"paths,omitempty"`   // needs_scope only: the paths asked for
	Counter string    `json:"counter,omitempty"` // unnamed block only: retries, returns or flap
	Detail  string    `json:"detail,omitempty"`  // what the CLI shows, at most 4 KB
	Until   time.Time `json:"until,omitzero"`    // retries only: when the override window lapses
}

// EventKey binds GroupBlocked to run-failure / group.blocked.
func (GroupBlocked) EventKey() Key { return Key{TypeRunFailure, "group.blocked"} }

// GroupFailed is the data of run-failure / group.failed: the group ran out of retries and no override came in time.
type GroupFailed struct {
	GroupFacts
	Prior  string `json:"prior,omitempty"` // the working stage the group left
	Last   string `json:"last"`            // the last failure: the exit reason or check that found the budget empty
	Detail string `json:"detail,omitempty"`
}

// EventKey binds GroupFailed to run-failure / group.failed.
func (GroupFailed) EventKey() Key { return Key{TypeRunFailure, "group.failed"} }

// RetrySpent is the data of run-status / retry.spent: a true failure cost the group one retry.
type RetrySpent struct {
	GroupFacts
	Left int `json:"left"` // retries the group has left
}

// EventKey binds RetrySpent to run-status / retry.spent.
func (RetrySpent) EventKey() Key { return Key{TypeRunStatus, "retry.spent"} }

// RetryWaived is the data of run-status / retry.waived: Komodo's own fault cost the group no retry.
type RetryWaived struct {
	GroupFacts
	Left int `json:"left"` // retries the group has left, unchanged
}

// EventKey binds RetryWaived to run-status / retry.waived.
func (RetryWaived) EventKey() Key { return Key{TypeRunStatus, "retry.waived"} }

// OverrideApproved is the data of run-status / override.approved: a person gave an empty group its final try.
type OverrideApproved struct {
	Granted int `json:"granted"` // tries added: always 1
	Left    int `json:"left"`    // tries the group has now
	Minutes int `json:"minutes"` // the working time the person gave the final try
}

// EventKey binds OverrideApproved to run-status / override.approved.
func (OverrideApproved) EventKey() Key { return Key{TypeRunStatus, "override.approved"} }

// OverrideLapsed is the data of run-status / override.lapsed: the window passed with no approval.
type OverrideLapsed struct {
	WindowMin int `json:"window_min"` // the window that passed, in minutes
}

// EventKey binds OverrideLapsed to run-status / override.lapsed.
func (OverrideLapsed) EventKey() Key { return Key{TypeRunStatus, "override.lapsed"} }

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
	Agent   string `json:"agent"`          // builder, qa, reviewer or planner
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
	ScopePaths  []string       `json:"scope_paths,omitempty"`  // needs_scope, or a self-denied done: the paths asked for
	ScopeReason string         `json:"scope_reason,omitempty"` // needs_scope, or a self-denied done: the builder's reason
	Verify      *VerifyOutcome `json:"verify,omitempty"`       // reviewer or Quality Assurance with reason done
	// builder with reason done: the commands it ran to prove its work
	Evidence []EvidenceRun `json:"evidence,omitempty"`
}

// EvidenceRun is one command an agent ran, its exit code and an excerpt of its output.
type EvidenceRun struct {
	Cmd     string `json:"cmd"`
	Exit    int    `json:"exit"`
	Excerpt string `json:"excerpt"`
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

// VerifyOutcome is a validated reviewer or Quality Assurance result.
type VerifyOutcome struct {
	Pass            bool     `json:"pass"`                       // every task met or confirmed and every item passed
	NotMet          []string `json:"not_met,omitempty"`          // task identifiers marked not_met or not_confirmed
	BriefIncomplete bool     `json:"brief_incomplete,omitempty"` // the reviewer answered brief_incomplete
	Failed          []string `json:"failed,omitempty"`           // Quality Assurance items marked failed
	// the score from 0 to 100 Quality Assurance gave each item
	Confidence []ItemConfidence `json:"confidence,omitempty"`
}

// ItemConfidence is the score Quality Assurance gave one acceptance item.
type ItemConfidence struct {
	Item       string `json:"item"`
	Confidence int    `json:"confidence"`
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
	Rule  string `json:"rule"`           // a rule identifier from a lens catalog
	File  string `json:"file"`           // path from the worktree root
	Line  int    `json:"line"`           // line number from 1 in the head tree
	Quote string `json:"quote"`          // the quoted evidence line
	Note  string `json:"note,omitempty"` // the agent's note on the finding
	// the Reviewer's own validation of the finding; absent when it gave none
	Proof      *FindingProof `json:"proof,omitempty"`
	Confidence int           `json:"confidence"` // 0 to 100: how sure the Reviewer is
}

// FindingProof is what the Reviewer ran or checked to validate a finding, and what it saw.
type FindingProof struct {
	Ran string `json:"ran"`
	Saw string `json:"saw"`
}

// EventKey binds FindingSubmit to run-status / finding.submit.
func (FindingSubmit) EventKey() Key { return Key{TypeRunStatus, "finding.submit"} }

// FindingVerified is the data of run-status / finding.verified.
type FindingVerified struct {
	FindingSubmit
	Level string `json:"level"` // critical, high, med_high, medium, med_low or low, from the rule
	In    string `json:"in"`    // "head" or "diff", where the quote was found
}

// EventKey binds FindingVerified to run-status / finding.verified.
func (FindingVerified) EventKey() Key { return Key{TypeRunStatus, "finding.verified"} }

// QAFailOpen is the data of run-status / qa.fail_open: the group went on with no Quality Assurance sign-off.
type QAFailOpen struct {
	GroupFacts
	Causes []string `json:"causes"` // why Quality Assurance could not test the group
}

// EventKey binds QAFailOpen to run-status / qa.fail_open.
func (QAFailOpen) EventKey() Key { return Key{TypeRunStatus, "qa.fail_open"} }

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
	Role   string `json:"role"`   // the agent whose commands moved, such as qa
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

// EpicPushed is the data of run-status / epic.pushed: the epic quiet, what its local branch holds and whether it was pushed.
type EpicPushed struct {
	Epic   string   `json:"epic"`   // the epic label, such as ep-1
	Branch string   `json:"branch"` // the epic branch
	Head   string   `json:"head"`   // commit id at the local epic tip
	Landed []string `json:"landed"` // labels of the groups landed on the local epic branch, in landing order
	Pushed bool     `json:"pushed"` // true when this push moved the remote branch
	Last   bool     `json:"last"`   // true when every group of the epic has landed
}

// EventKey binds EpicPushed to run-status / epic.pushed.
func (EpicPushed) EventKey() Key { return Key{TypeRunStatus, "epic.pushed"} }

// PROpened is the data of run-status / pr.opened: the epic's pull request is open after a push.
type PROpened struct {
	Epic   string `json:"epic"`   // the epic label
	Branch string `json:"branch"` // the epic branch
	Head   string `json:"head"`   // commit id at the pushed epic tip
	PRURL  string `json:"pr_url"` // the pull request address
	PRNew  bool   `json:"pr_new"` // true when this push opened the pull request
}

// EventKey binds PROpened to run-status / pr.opened.
func (PROpened) EventKey() Key { return Key{TypeRunStatus, "pr.opened"} }

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

// RiskSet is the data of run-status / risk.set: the risk level Go set for a group from its diff, and the rule that set it.
type RiskSet struct {
	GroupFacts
	Level  string `json:"level"`  // low, med_low, medium, med_high, high or critical
	Reason string `json:"reason"` // the rule that set the level
}

// EventKey binds RiskSet to run-status / risk.set.
func (RiskSet) EventKey() Key { return Key{TypeRunStatus, "risk.set"} }

// DeepDone is the data of run-status / deep.done: the outcome of the deep checks of a group's risk level.
type DeepDone struct {
	GroupFacts
	Pass    bool        `json:"pass"` // no check failed; a warning does not count
	Results []DeepCheck `json:"results"`
}

// EventKey binds DeepDone to run-status / deep.done.
func (DeepDone) EventKey() Key { return Key{TypeRunStatus, "deep.done"} }

// DeepCheck is one deep check's result.
type DeepCheck struct {
	Kind    string `json:"kind"`              // race, fuzz, mutation or bench
	Pass    bool   `json:"pass"`              // false when the check failed
	Note    string `json:"note,omitempty"`    // a warning or a skip reason
	Mutants int    `json:"mutants,omitempty"` // surviving mutants recorded
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

// CommandDropped is the data of run-status / command.dropped: the Task Manager ignored a command.
type CommandDropped struct {
	Kind string `json:"kind"` // the command kind that was ignored
	Re   string `json:"re"`   // the request the command answered; empty when none
	Why  string `json:"why"`  // why the Task Manager ignored it
}

// EventKey binds CommandDropped to run-status / command.dropped.
func (CommandDropped) EventKey() Key { return Key{TypeRunStatus, "command.dropped"} }

// SandboxWarning is the data of run-status / sandbox.warning: an outside tool server started with a warning.
type SandboxWarning struct {
	Warning string `json:"warning"` // the warning text the server connection reported
}

// EventKey binds SandboxWarning to run-status / sandbox.warning.
func (SandboxWarning) EventKey() Key { return Key{TypeRunStatus, "sandbox.warning"} }

// ScopeRequest is the data of run-status / scope.request: a Builder asked to edit files outside its list.
type ScopeRequest struct {
	Task  string   `json:"task"`  // the task the Builder was working on
	Paths []string `json:"paths"` // the paths it asked for
}

// EventKey binds ScopeRequest to run-status / scope.request.
func (ScopeRequest) EventKey() Key { return Key{TypeRunStatus, "scope.request"} }

// ScopeAnswer is the data of run-status / scope.answer: the answer to a scope request.
type ScopeAnswer struct {
	Action   string   `json:"action"`             // approve or deny
	Approved []string `json:"approved,omitempty"` // the paths added to the group's files
	Auto     bool     `json:"auto,omitempty"`     // true when the scope rules denied the request without asking a person
}

// EventKey binds ScopeAnswer to run-status / scope.answer.
func (ScopeAnswer) EventKey() Key { return Key{TypeRunStatus, "scope.answer"} }

// GroupPause is the data of run-status / group.pause: the Task Manager parked a session on a provider limit.
type GroupPause struct {
	Link      string    `json:"link"`       // the link whose limit stopped the session
	WaitUntil time.Time `json:"wait_until"` // when the Task Manager next tries to resume
	Agent     string    `json:"agent"`      // the agent whose session was parked
}

// EventKey binds GroupPause to run-status / group.pause.
func (GroupPause) EventKey() Key { return Key{TypeRunStatus, "group.pause"} }

// GroupResume is the data of run-status / group.resume: the Task Manager left a parked session's wait.
type GroupResume struct {
	Reason string   `json:"reason"` // timer or run-continue
	Owed   []string `json:"owed"`   // the agents whose sessions were parked and now restart
}

// EventKey binds GroupResume to run-status / group.resume.
func (GroupResume) EventKey() Key { return Key{TypeRunStatus, "group.resume"} }

// GroupStop is the data of run-status / group.stop: the Task Manager was told to stop.
type GroupStop struct {
	Cause string `json:"cause"` // why it was stopped, such as a block reason
}

// EventKey binds GroupStop to run-status / group.stop.
func (GroupStop) EventKey() Key { return Key{TypeRunStatus, "group.stop"} }

// GroupClosed is the data of run-status / group.closed: the Task Manager finished closing a group; it has no body.
type GroupClosed struct{}

// EventKey binds GroupClosed to run-status / group.closed.
func (GroupClosed) EventKey() Key { return Key{TypeRunStatus, "group.closed"} }

// JudgeReset is the data of run-status / judge.reset: a judge's command edited tracked files and the round was discarded.
type JudgeReset struct {
	Edited []string `json:"edited"` // the tracked files the judges' commands changed
}

// EventKey binds JudgeReset to run-status / judge.reset.
func (JudgeReset) EventKey() Key { return Key{TypeRunStatus, "judge.reset"} }

// CleanupSwept is the data of run-status / cleanup.swept: what one sweep removed and kept.
type CleanupSwept struct {
	Group   string        `json:"group"` // empty: a sweep covers no single group
	Removed []CleanupItem `json:"removed"`
	Kept    []CleanupItem `json:"kept"`
}

// CleanupItem is one thing a sweep removed or kept.
type CleanupItem struct {
	Kind   string `json:"kind"`   // worktree, qa_worktree, container, branch, temp or version
	Name   string `json:"name"`   // the path, branch, reference or version
	Reason string `json:"reason"` // why it was kept; empty for a removed item
}

// EventKey binds CleanupSwept to run-status / cleanup.swept.
func (CleanupSwept) EventKey() Key { return Key{TypeRunStatus, "cleanup.swept"} }

// LimitWarn is the data of resource / limit.warn.
type LimitWarn struct {
	GroupFacts
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
