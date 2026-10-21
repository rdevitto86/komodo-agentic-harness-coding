package task

import (
	"context"
	"errors"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cache"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/sandbox"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// Brief is the assembled prompt, one text per part, and its size in tokens.
type Brief struct {
	Agent  string
	Parts  [5]string // tools, role, repository guidance, standards, task
	Tokens int
}

// Briefer assembles a brief for one agent from slot values.
type Briefer interface {
	Brief(ctx context.Context, agent string, slots map[string]string) (Brief, error)
}

// RoleInput is what a role needs to fill its brief.
type RoleInput struct {
	Group    string
	Tasks    []TaskSpec
	Diff     string
	Trip     int
	Findings []string
	// added lines no test ran, as file:line-range
	Uncovered []string
	// the task whose edits an earlier builder session left in the worktree; nil when none
	Unfinished *state.UnfinishedEdits
}

// Role is the agent-specific half of a stage.
type Role interface {
	// Slots returns the brief values for this trip, from the group's state and earlier findings.
	Slots(ctx context.Context, in RoleInput) (map[string]string, error)
	// Judge validates a session's result, runs the role's own gate and says what happens next.
	Judge(ctx context.Context, r llm.Result) (Outcome, error)
}

// FindingSink is implemented by a role that collects the findings its session files.
type FindingSink interface {
	// Submit receives one finding filed by submit_finding.
	Submit(f Submitted)
}

// RunSink is implemented by a role that logs the commands its session ran with the run tool.
type RunSink interface {
	// Ran receives one command the session ran.
	Ran(cmd string)
}

// ItemSource is implemented by a role that knows the group's acceptance items.
type ItemSource interface {
	// AcceptanceItems returns the acceptance items of the group's tasks.
	AcceptanceItems() []agent.Item
}

// ProofChecker is implemented by a role that can tell, at submit, whether a finding's proof would be refused.
type ProofChecker interface {
	// CheckProof returns why a proof is refused, or nil when it may run.
	CheckProof(p agent.Proof) error
}

// GateError is a rejected result: Text goes back into the live session, Schema marks a result that broke its schema.
type GateError struct {
	Text   string
	Schema bool
}

// Error returns the text sent back to the session.
func (e *GateError) Error() string { return e.Text }

// Outcome is what a role's Judge decides.
type Outcome struct {
	Kind     OutcomeKind
	Reason   string   // for Block, and the builder's reason for Scope
	Findings []string // for Return: verified findings the builder receives; for Restart: Go's output
	Paths    []string // for Scope: the paths the builder asked for
	Summary  []string // for Pass: the result lines the pull request body prints
	// for the reviewer: every finding Go kept, with its level and proof; Recorded holds the run-record-only lines
	Verified []VerifiedFinding
	Recorded []string
}

// VerifiedFinding is a reviewer finding Go kept, with the level and proof result Go gave it.
type VerifiedFinding struct {
	Rule   string             `json:"rule"`
	File   string             `json:"file"`
	Line   int                `json:"line"`
	Quote  string             `json:"quote"`
	Note   string             `json:"note,omitempty"`
	Level  agent.FindingLevel `json:"level"`
	In     string             `json:"in"`    // head or diff, where the quote was found
	Proof  string             `json:"proof"` // failed_as_claimed, passed or none
	Bucket agent.Bucket       `json:"-"`
}

// OutcomeKind is the pipeline's next move for one role.
type OutcomeKind string

// Git is the shared git wrapper as the Task Manager uses it; every call follows the host git rules.
type Git interface {
	TreeHash(ctx context.Context, l LeaseRef) (string, error)
	Diff(ctx context.Context, l LeaseRef, paths []string) (string, error)
	ReadOnly(
		ctx context.Context,
		l LeaseRef,
		command string,
		args []string,
	) (string, error) // log, show, blame, status
	ResetTracked(
		ctx context.Context,
		l LeaseRef,
	) error // restores changed tracked files; removes untracked, non-ignored files
	AddWorktree(
		ctx context.Context,
		l LeaseRef,
	) error // creates l.Branch and checks it out at l.Worktree
}

// PointerRecorder is implemented by a Git that records the worktree's .git pointer when the group starts.
type PointerRecorder interface {
	// RecordPointer keeps the exact text of the worktree's .git file for the later checks.
	RecordPointer(l LeaseRef) error
}

// LimitStore is the machine-wide record of limited links and link session counts.
type LimitStore interface {
	ResetAt(l llm.Link) (time.Time, bool) // when the link's limit lifts; false when not limited
	Record(l llm.Link, reset time.Time, cap llm.CapKind) error
	// TakeSlot counts one more live session on the link; false when the link already has max.
	TakeSlot(l llm.Link, max int) (bool, error)
	// ReleaseSlot counts one session fewer on the link.
	ReleaseSlot(l llm.Link) error
}

// SandboxPlan holds the folders a sandbox instance is built from.
type SandboxPlan struct {
	Temp       string   // folder that holds each instance's temporary folder; a system temporary folder when empty
	Build      string   // the group's private build cache; writable
	Modules    string   // module and registry caches; read-only
	Toolchains []string // toolchain folders; read-only
	MaxProcs   int
	MaxMemGB   int
}

// Collaborators are the parts a Manager calls.
type Collaborators struct {
	Runtime     Runtime         // the Runtime Manager; when nil, Serve supplies one over the reply channel
	Briefs      Briefer         // the Context Manager, through the Runtime Manager
	Roles       map[string]Role // the builder, Quality Assurance and reviewer
	Git         Git             // the shared git wrapper
	Commit      Committer       // the Commit Handler
	Router      *Router
	Client      *llm.Client
	Sandboxes   *sandbox.Manager
	Limits      LimitStore
	Cache       cache.Handle        // the cache handle; only the Runtime Manager creates or closes the cache
	Checks      agent.Checker       // the mechanical checks run before Quality Assurance and Reviewer start
	Deep        agent.Deep          // the deep checks of the group's risk level; nil runs none
	Commands    map[string][]string // command class to argv: setup, build, test, lint, format
	Base        string              // the commit the group's checks diff against
	Language    string              // decides the impact set of the checks
	Meta        GroupMeta           // what the Commit Handler needs besides the spec
	Plan        SandboxPlan         // where the sandbox instances keep their files
	SessionsDir string              // folder that holds one folder per session
	Exe         string              // the komodo program, which the subscription link starts as its tool server
	Socket      string              // the engine's control socket, which that tool server relays every call to
	Tools       *ToolRoutes         // where each subscription session's bound tools are reachable by the engine
	Skill       func(ref string) (string, error)
	SkipReview  func(diff string) bool         // true when the group's risk level takes no Reviewer; nil never skips
	Getenv      func(string) string            // environment of outside tool servers; os.Getenv when nil
	OnRecheck   func(pass bool, output string) // receives the result of the recheck command
	Now         func() time.Time               // clock; time.Now when nil
	Tick        time.Duration                  // session timer period; TickEvery when zero
	BeatEvery   time.Duration                  // how often Serve sends a beat message; the package BeatEvery when zero
	Ladder      [2]time.Duration               // stop ladder waits (to SIGTERM, to kill); TermAfter and KillAfter when zero
	ResumeWait  func(try int) time.Duration    // backoff after a refused resume; 15, 30, 60 minutes when nil
}

// The outcome kinds.
const (
	OutcomePass    OutcomeKind = "pass"    // this role approves
	OutcomeReturn  OutcomeKind = "return"  // back to Build with Findings
	OutcomeRestart OutcomeKind = "restart" // start the same role once more (a disputed Quality Assurance result)
	OutcomeBlock   OutcomeKind = "block"   // escalate with Reason
	OutcomeScope   OutcomeKind = "scope"   // the builder asks for files outside its list
)

// Errors Run, Resume and Continue return.
var (
	ErrHeld         = errors.New("an escalation is open")
	ErrGroupPaused  = errors.New("the group waits on a provider limit")
	ErrNoEscalation = errors.New("no open escalation matches the answer")
	ErrNotPaused    = errors.New("the group is not paused")
)
