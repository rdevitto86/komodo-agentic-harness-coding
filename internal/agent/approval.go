package agent

import "slices"

// Block reasons Decide returns.
const (
	BlockFlaky = "flaky" // Quality Assurance's two reruns of an item disagreed
	BlockQA    = "qa"    // Quality Assurance disputed Go's reruns a second time
	BlockBrief = "brief" // the Reviewer found the brief incomplete
)

// ReviewerVerdict is the Reviewer's outcome as the approval rule reads it.
type ReviewerVerdict struct {
	Ended           bool      `json:"ended"`                      // the Reviewer's session ended with a valid result
	NotMet          []string  `json:"not_met,omitempty"`          // task identifiers marked not_met
	Blocking        []Finding `json:"blocking,omitempty"`         // verified findings of severity blocker or major
	BriefIncomplete bool      `json:"brief_incomplete,omitempty"` // any task answered brief_incomplete
}

// Decision is what the approval rule decides for a round.
type Decision string

const (
	DecisionWait    Decision = "wait"    // a judge has not finished, or Quality Assurance is being restarted
	DecisionApprove Decision = "approve" // both passed: hand the approved work to the Commit Handler
	DecisionReturn  Decision = "return"  // back to the Builder with findings
	DecisionBlock   Decision = "block"   // a harness fault or a flaky item; a person is needed
)

// Outcome is the result of Decide.
type Outcome struct {
	Decision Decision  `json:"decision"`
	Reason   string    `json:"reason,omitempty"`   // the block reason when Decision is block
	Findings []Finding `json:"findings,omitempty"` // every verified blocking finding, for the Builder
	Items    []string  `json:"items,omitempty"`    // Quality Assurance items Go confirmed failed
	NotMet   []string  `json:"not_met,omitempty"`  // tasks marked not_met or not_confirmed
}

// Decide joins Quality Assurance's and the Reviewer's verdicts for one round.
// Faults block at once; a round returns only after the Reviewer has ended, and approves only when both judges ended clean.
func Decide(t QAVerdict, r ReviewerVerdict) Outcome {
	switch {
	case r.Ended && r.BriefIncomplete:
		return Outcome{Decision: DecisionBlock, Reason: BlockBrief}
	case len(t.Split) > 0:
		return Outcome{Decision: DecisionBlock, Reason: BlockFlaky}
	case len(t.Disputed) > 0 && t.Restarts >= QARestarts:
		return Outcome{Decision: DecisionBlock, Reason: BlockQA}
	case !r.Ended:
		return Outcome{Decision: DecisionWait}
	}

	out := Outcome{Findings: r.Blocking, NotMet: unionSorted(t.NotMet, r.NotMet)}
	if t.Ended {
		out.Items = t.Failed
	}

	switch {
	case len(r.NotMet) > 0 || len(r.Blocking) > 0:
		out.Decision = DecisionReturn
	case !t.Ended || len(t.Disputed) > 0:
		return Outcome{Decision: DecisionWait}
	case !t.Pass || len(t.NotMet) > 0 || len(t.Failed) > 0:
		out.Decision = DecisionReturn
	default:
		return Outcome{Decision: DecisionApprove}
	}

	return out
}

// unionSorted returns the sorted union of two lists without repeats.
func unionSorted(a, b []string) []string {
	all := append(slices.Clone(a), b...)
	slices.Sort(all)
	return slices.Compact(all)
}
