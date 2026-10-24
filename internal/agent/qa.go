package agent

import (
	"errors"
	"fmt"
	"strings"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

const (
	FlakeReruns       = 2  // solo reruns of a failing test
	SignoffConfidence = 80 // the least confidence at which a passed item is signed off
)

// Confirmation is Quality Assurance's answer for one task identifier.
type Confirmation string

const (
	Confirmed    Confirmation = "confirmed"
	NotConfirmed Confirmation = "not_confirmed"
)

// QAStatus says whether Quality Assurance could test the group.
type QAStatus string

const (
	QATested     QAStatus = "tested"
	QAUntestable QAStatus = "untestable"
)

// Item is one acceptance item of a group's task list.
type Item struct {
	ID    string
	Text  string   // the acceptance sentence
	Cmd   string   // the command that checks the item; empty when the item has none
	Needs []string // network, docker or service:<name>
}

// EvidenceRun is one command Quality Assurance ran for an item, reported as data.
type EvidenceRun struct {
	Cmd     string `json:"cmd" desc:"The command as run."`
	Exit    int    `json:"exit" desc:"The exit code you saw."`
	Excerpt string `json:"excerpt" desc:"The output lines that show the result, up to 4 KB, head and tail."`
}

// ItemResult is Quality Assurance's pass or fail for one item.
type ItemResult struct {
	Item string `json:"item" desc:"The acceptance item identifier from the brief."`
	Pass bool   `json:"pass" desc:"True when the item holds."`
	// the runs behind the answer; at least one
	Evidence []EvidenceRun `json:"evidence" desc:"One entry per command you ran for the item; never empty."`
	// how sure Quality Assurance is of the answer, after it checked the item itself
	Confidence *int `json:"confidence" desc:"0 to 100: how sure you are of this answer; 0 not confident, 100 certain."`
}

// TaskConfirmation is Quality Assurance's answer for one task identifier.
type TaskConfirmation struct {
	Task         string       `json:"task" desc:"The task identifier from the brief."`
	Confirmation Confirmation `json:"confirmation" desc:"confirmed when its acceptance holds, else not_confirmed."`
}

// QAResult is Quality Assurance's whole final call.
type QAResult struct {
	Status QAStatus           `json:"status" desc:"tested when you judged the items, else untestable."`
	Cause  string             `json:"cause,omitempty" desc:"Only with untestable: what was not ready and what you tried."`
	Items  []ItemResult       `json:"items" desc:"Exactly one entry per acceptance item, as an array of objects."`
	Tasks  []TaskConfirmation `json:"tasks" desc:"Exactly one entry per task identifier, as an array of objects."`
}

// QAVerdict is Quality Assurance's outcome as the approval rule reads it.
type QAVerdict struct {
	Ended      bool           `json:"ended"`                // Quality Assurance ended with a valid result
	Pass       bool           `json:"pass"`                 // every task confirmed and items passed or skipped
	NotMet     []string       `json:"not_met,omitempty"`    // task identifiers marked not_confirmed
	Failed     []string       `json:"failed,omitempty"`     // items Quality Assurance marked failed
	Skipped    []string       `json:"skipped,omitempty"`    // items with needs: or no command, never blocking
	Confidence map[string]int `json:"confidence,omitempty"` // item identifier to the score Quality Assurance gave it
}

// Quality Assurance is the Go side of a Quality Assurance session.
type QA interface {
	// AcceptResult rejects a result that skips or repeats a task identifier or an acceptance item, leaves evidence empty,
	// or gives a confidence outside 0 to 100.
	AcceptResult(tasks, items []string, r QAResult) error
}

// QARole returns the compiled Quality Assurance role.
func QARole() Role {
	return newRole("qa",
		[]string{"read", "search", "run", "run_check"},
		[]string{"build", "test"},
		[]string{"R1"},
		[]string{"repo_profile", "group", "tasks", "acceptance"},
		[]string{"repo_context", "standards", "diff", "checks", "baseline", "builder_evidence"},
		"komodo:agent/qa/result/1", QAResult{}, true)
}

// NewQA returns the Go side of a Quality Assurance session.
func NewQA() QA { return goQA{} }

// goQA is Quality Assurance with no state of its own.
type goQA struct{}

// ParseQAResult decodes a Quality Assurance final call, refusing unknown fields.
func ParseQAResult(raw []byte) (QAResult, error) {
	var r QAResult
	if err := core.DecodeStrict(raw, &r); err != nil {
		return QAResult{}, fmt.Errorf("decode Quality Assurance result: %w", err)
	}
	return r, nil
}

// AcceptResult rejects a result that skips or repeats a task identifier or an acceptance item,
// leaves evidence empty, or gives no confidence or one outside 0 to 100.
func (goQA) AcceptResult(tasks, items []string, r QAResult) error {
	switch r.Status {
	case QAUntestable:
		if strings.TrimSpace(r.Cause) == "" {
			return errors.New("an untestable result needs a cause: what was not ready and what you tried")
		}
		return nil
	case QATested:
	default:
		return fmt.Errorf("result has unknown status %q", r.Status)
	}

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
		if len(it.Evidence) == 0 {
			return fmt.Errorf("item %s has no evidence", it.Item)
		}
		for _, e := range it.Evidence {
			if strings.TrimSpace(e.Cmd) == "" {
				return fmt.Errorf("item %s has an evidence run with no command", it.Item)
			}
			if len(e.Excerpt) > GateOutputCap {
				return fmt.Errorf("item %s has an excerpt over %d bytes; keep the lines that show the result", it.Item,
					GateOutputCap)
			}
		}
		if it.Confidence == nil {
			return fmt.Errorf("item %s has no confidence; give a score from 0 to 100", it.Item)
		}
		if !ValidConfidence(*it.Confidence) {
			return fmt.Errorf("item %s has confidence %d outside 0 to 100", it.Item, *it.Confidence)
		}
	}
	return nil
}
