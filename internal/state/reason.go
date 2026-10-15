package state

// Reason is the one closed list of why a group is blocked and why a session ended; this package owns it, and the Runtime Manager and the Task Manager use it.
type Reason string

const (
	ReasonNoProvider    Reason = "no_provider"    // every link in the chain was unavailable or unauthorized
	ReasonPauseTimeout  Reason = "pause_timeout"  // paused for limits.pause_max_min
	ReasonPaidRefused   Reason = "paid_refused"   // the billing check refused a paid link
	ReasonRegression    Reason = "regression"     // a test that passed at the base fails and returns are spent
	ReasonReturns       Reason = "returns"        // returns are spent
	ReasonFlap          Reason = "flap"           // the same failure, or a return that changed nothing
	ReasonRetries       Reason = "retries"        // an agent's timeouts or idle stops passed its retries
	ReasonAttempts      Reason = "attempts"       // every fresh builder session was used
	ReasonNeedsScope    Reason = "needs_scope"    // the builder needs a file outside its group's files
	ReasonContext       Reason = "context"        // the brief exceeds half the context of every link
	ReasonSetup         Reason = "setup"          // the group could not start: lease, worktree or sandbox
	ReasonPublish       Reason = "publish"        // push or pull request failed
	ReasonMergeConflict Reason = "merge_conflict" // the group's branch conflicts with its base
	ReasonCleanup       Reason = "cleanup"        // cleanup could not remove what it must
	ReasonWall          Reason = "wall"           // working time reached twice limits.group_wall_min
	ReasonDependency    Reason = "dependency"     // a dependency was removed from the run
	ReasonCrash         Reason = "crash"          // the group's goroutine panicked, or an action failed past its retry
	ReasonContract      Reason = "contract"       // a decision result broke the event's declared shape
	ReasonStateInvalid  Reason = "state_invalid"  // a record or a write failed a check
	ReasonTester        Reason = "tester"         // the Tester's results were disputed twice
	ReasonFlaky         Reason = "flaky"          // the Tester's reruns split once and a role reported results it cannot confirm
	ReasonDone          Reason = "done"           // session exit: the session returned a valid result
	ReasonCheckFail     Reason = "check_fail"     // session exit: the result failed the agent's own gate twice
	ReasonNoResult      Reason = "no_result"      // session exit: the reply was cut off or the conversation outgrew the context
	ReasonSchemaFail    Reason = "schema_fail"    // session exit: the result broke the schema after the one re-prompt
	ReasonTimeout       Reason = "timeout"        // session exit: the wall limit stopped the session
	ReasonIdle          Reason = "idle"           // session exit: the session stayed silent past the nudge and its grace
	ReasonRefusals      Reason = "refusals"       // session exit: the guard refused one row the limit's number of times
	ReasonProvider      Reason = "provider"       // session exit: a usage limit ended it and another link is allowed
	ReasonBilling       Reason = "billing"        // session exit: the first stream line showed a paid key
	ReasonPaused        Reason = "paused"         // session exit: a limit halted it, no link is left, a resume handle exists
	ReasonCancelled     Reason = "cancelled"      // session exit: the Task Manager stopped it on purpose
)
