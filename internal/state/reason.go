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

	// a test that passed at the base fails and returns are spent
	ReasonRegression Reason = "regression"

	// returns are spent
	ReasonReturns Reason = "returns"

	// the same failure, or a return that changed nothing
	ReasonFlap Reason = "flap"

	// an agent's timeouts or idle stops passed its retries
	ReasonRetries Reason = "retries"

	// every fresh builder session was used
	ReasonAttempts Reason = "attempts"

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

	// working time reached twice limits.group_wall_min
	ReasonWall Reason = "wall"

	// a dependency was removed from the run
	ReasonDependency Reason = "dependency"

	// the group's goroutine panicked, or an action failed past its retry
	ReasonCrash Reason = "crash"

	// a decision result broke the event's declared shape
	ReasonContract Reason = "contract"

	// a record or a write failed a check
	ReasonStateInvalid Reason = "state_invalid"

	// the Tester's results were disputed twice
	ReasonTester Reason = "tester"

	// the Tester's reruns split once and a role reported results it cannot confirm
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
