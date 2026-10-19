package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

const (
	FlakeReruns    = 2 // solo reruns of a failing test or a failed Tester item
	TesterRestarts = 1 // restarts of the Tester after a dispute
)

// DisputeReason is why the Tester failed an item that Go's reruns passed.
type DisputeReason string

const (
	ReasonEnv          DisputeReason = "env"
	ReasonWrongCommand DisputeReason = "wrong_command"
	ReasonFlaky        DisputeReason = "flaky"
	ReasonRealFailure  DisputeReason = "real_failure"
)

// Confirmation is the Tester's answer for one task identifier.
type Confirmation string

const (
	Confirmed    Confirmation = "confirmed"
	NotConfirmed Confirmation = "not_confirmed"
)

// Item is one acceptance item of a group's task list.
type Item struct {
	ID    string
	Text  string   // the acceptance sentence
	Cmd   string   // the command Go can rerun; empty when the item has none
	Needs []string // network, docker or service:<name>
}

// ItemResult is the Tester's pass or fail for one item.
type ItemResult struct {
	Item string `json:"item" desc:"The acceptance item identifier from the brief."`
	Pass bool   `json:"pass" desc:"True when the item holds."`
	// the output or observation behind the answer
	Evidence string `json:"evidence" desc:"The command output or observation behind the answer; never empty."`
	// after a restart with Go's output: why the item failed for the Tester
	Reason DisputeReason `json:"reason,omitempty" desc:"Only after a restart that shows rerun output: why the item failed."`
}

// TaskConfirmation is the Tester's answer for one task identifier.
type TaskConfirmation struct {
	Task         string       `json:"task" desc:"The task identifier from the brief."`
	Confirmation Confirmation `json:"confirmation" desc:"confirmed when its acceptance holds, else not_confirmed."`
}

// TesterResult is the Tester's whole final call.
type TesterResult struct {
	Items []ItemResult       `json:"items" desc:"Exactly one entry per acceptance item, as an array of objects."`
	Tasks []TaskConfirmation `json:"tasks" desc:"Exactly one entry per task identifier, as an array of objects."`
}

// RerunOutcome is Go's judgement of a failed item after two reruns.
type RerunOutcome string

const (
	RerunConfirmed   RerunOutcome = "confirmed"   // both reruns failed
	RerunDisputed    RerunOutcome = "disputed"    // both reruns passed
	RerunSplit       RerunOutcome = "split"       // one passed, one failed
	RerunUnconfirmed RerunOutcome = "unconfirmed" // the item has no command, so Go ran nothing
)

// TesterVerdict is the Tester's outcome as the approval rule reads it.
type TesterVerdict struct {
	Ended    bool     `json:"ended"`              // the Tester's session ended with a valid result and Go's reruns finished
	Restarts int      `json:"restarts,omitempty"` // restarts spent after a dispute
	Pass     bool     `json:"pass"`               // every task confirmed and every item passed or skipped
	NotMet   []string `json:"not_met,omitempty"`  // task identifiers marked not_confirmed
	Failed   []string `json:"failed,omitempty"`   // items Go's two reruns confirmed failed
	Disputed []string `json:"disputed,omitempty"` // items Go's two reruns passed
	Split    []string `json:"split,omitempty"`    // items whose two reruns disagreed
	Skipped  []string `json:"skipped,omitempty"`  // items with needs: or no command, never blocking
}

// Tester is the Go side of a Tester session.
type Tester interface {
	// Rerun runs an item's command twice in the sandbox and returns the outcome and the last output.
	Rerun(ctx context.Context, run CommandRunner, it Item) (RerunOutcome, string, error)
	// AcceptResult rejects a result that skips or repeats a task identifier or an acceptance item, or leaves evidence empty.
	AcceptResult(tasks, items []string, r TesterResult) error
}

// TesterRole returns the compiled Tester role.
func TesterRole() Role {
	return newRole("tester",
		[]string{"read", "search", "run"},
		[]string{"build", "test"},
		[]string{"R1"},
		[]string{"repo_profile", "group", "tasks", "acceptance"},
		[]string{"repo_context", "standards", "diff", "checks", "baseline"},
		"komodo:agent/tester/result/1", TesterResult{}, true)
}

// NewTester returns the Go side of a Tester session.
func NewTester() Tester { return goTester{} }

// goTester is the Tester with no state of its own.
type goTester struct{}

// ParseTesterResult decodes a Tester final call, refusing unknown fields.
func ParseTesterResult(raw []byte) (TesterResult, error) {
	var r TesterResult
	if err := core.DecodeStrict(raw, &r); err != nil {
		return TesterResult{}, fmt.Errorf("decode tester result: %w", err)
	}
	return r, nil
}

// AcceptResult rejects a result that skips or repeats a task identifier or an acceptance item,
// leaves evidence empty or uses a word outside the schema.
func (goTester) AcceptResult(tasks, items []string, r TesterResult) error {
	got := make([]string, len(r.Tasks))
	for i, t := range r.Tasks {
		got[i] = t.Task
		if t.Confirmation != Confirmed && t.Confirmation != NotConfirmed {
			return fmt.Errorf("task %s has unknown confirmation %q", t.Task, t.Confirmation)
		}
	}
	if err := ExactlyOnce(tasks, got); err != nil {
		return err
	}

	if len(got) != len(tasks) {
		return errors.New("result names a task that is not in the group")
	}

	gotItems := make([]string, len(r.Items))
	for i, it := range r.Items {
		gotItems[i] = it.Item
	}
	if err := onceOf("acceptance item", items, gotItems); err != nil {
		return err
	}
	if len(gotItems) != len(items) {
		return errors.New("result names an item that is not an acceptance item of the group")
	}

	for _, it := range r.Items {
		if strings.TrimSpace(it.Item) == "" {
			return errors.New("an item result has no item identifier")
		}
		if strings.TrimSpace(it.Evidence) == "" {
			return fmt.Errorf("item %s has no evidence", it.Item)
		}
		switch it.Reason {
		case "", ReasonEnv, ReasonWrongCommand, ReasonFlaky, ReasonRealFailure:
		default:
			return fmt.Errorf("item %s has unknown reason %q", it.Item, it.Reason)
		}
	}
	return nil
}
