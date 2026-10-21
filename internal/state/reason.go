package state

// Reason is the one closed list of why a group is blocked and why a session ended; this package owns it, and the Runtime
// Manager and the Task Manager use it.
type Reason string

const (
	// every link in the chain was unavailable or unauthorized
	ReasonNoProvider Reason = "no_provider"

	// paused for limits.pause_max_min
	ReasonPauseTimeout Reason = "pause_timeout"

	// the billing check refused a paid link
	ReasonPaidRefused Reason = "paid_refused"

	// the same failure, or a return that changed nothing
	ReasonFlap Reason = "flap"

	// the group's retries are spent; a person may override until the window lapses
	ReasonRetries Reason = "retries"

	// the builder needs a file outside its group's files
	ReasonNeedsScope Reason = "needs_scope"

	// the brief exceeds half the context of every link
	ReasonContext Reason = "context"

	// the group could not start: lease, worktree or sandbox
	ReasonSetup Reason = "setup"

	// push or pull request failed
	ReasonPublish Reason = "publish"

	// the group's branch conflicts with its base
	ReasonMergeConflict Reason = "merge_conflict"

	// cleanup could not remove what it must
	ReasonCleanup Reason = "cleanup"

	// legacy: a group saved before wall time was charged per try; a plain run continues it
	ReasonWall Reason = "wall"

	// a dependency was removed from the run
	ReasonDependency Reason = "dependency"

	// the group's goroutine panicked, or a person must look after four blocker errors in a row
	ReasonCrash Reason = "crash"

	// a decision result broke the event's declared shape
	ReasonContract Reason = "contract"

	// a record or a write failed a check
	ReasonStateInvalid Reason = "state_invalid"

	// Quality Assurance's reruns split once and a role reported results it cannot confirm
	ReasonFlaky Reason = "flaky"

	// session exit: the session returned a valid result
	ReasonDone Reason = "done"

	// session exit: the result failed the agent's own gate twice
	ReasonCheckFail Reason = "check_fail"

	// session exit: the reply was cut off or the conversation outgrew the context
	ReasonNoResult Reason = "no_result"

	// session exit: the result broke the schema after the one re-prompt
	ReasonSchemaFail Reason = "schema_fail"

	// session exit: the wall limit stopped the session
	ReasonTimeout Reason = "timeout"

	// session exit: the session stayed silent past the nudge and its grace
	ReasonIdle Reason = "idle"

	// session exit: the guard refused one row the limit's number of times
	ReasonRefusals Reason = "refusals"

	// session exit: a usage limit ended it and another link is allowed
	ReasonProvider Reason = "provider"

	// session exit: the first stream line showed a paid key
	ReasonBilling Reason = "billing"

	// session exit: a limit halted it, no link is left, a resume handle exists
	ReasonPaused Reason = "paused"

	// session exit: the Task Manager stopped it on purpose
	ReasonCancelled Reason = "cancelled"
)

// Charged reports whether a session that ended for r was a true failure of the work, the only kind that spends a retry.
func (r Reason) Charged() bool {
	switch r {
	case ReasonTimeout, ReasonIdle, ReasonCheckFail, ReasonNoResult, ReasonSchemaFail, ReasonRefusals:
		return true
	}

	return false
}

// Severity ranks how serious a reason is for Komodo itself.
type Severity string

const (
	// SeverityBlocker means an error stops an agent session from working on its tasks; only this tier counts toward a breaker
	SeverityBlocker Severity = "blocker"

	// SeverityFault means a step after the work broke; it is recorded by type and counts toward no breaker
	SeverityFault Severity = "fault"

	// SeverityMinor means expected and self-healing; it counts nowhere
	SeverityMinor Severity = "minor"

	// SeverityWork means the work failed; it spends a retry and is no fault
	SeverityWork Severity = "work"

	// SeverityNone means the reason says nothing about Komodo's health
	SeverityNone Severity = ""
)

// Severity returns the tier of a reason; Classify is the one table.
func (r Reason) Severity() Severity { return Classify(r) }

// Classify is the one table of tiers; a session crash and a goroutine panic are both blockers.
func Classify(r Reason) Severity {
	switch r {
	case ReasonCrash, ReasonStateInvalid, ReasonContract, ReasonSetup:
		return SeverityBlocker
	case ReasonPublish, ReasonCleanup:
		return SeverityFault
	case ReasonCancelled, ReasonProvider, ReasonPaused, ReasonBilling:
		return SeverityMinor
	}
	if r.Charged() {
		return SeverityWork
	}

	return SeverityNone
}
