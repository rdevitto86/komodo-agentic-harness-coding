package agent

import (
	"errors"
	"fmt"
	"strings"
)

// Verdict is the Reviewer's answer for one task identifier.
type Verdict string

const (
	VerdictMet             Verdict = "met"
	VerdictNotMet          Verdict = "not_met"
	VerdictBriefIncomplete Verdict = "brief_incomplete"
)

// TaskVerdict is the Reviewer's verdict on one task.
type TaskVerdict struct {
	Task    string  `json:"task"`
	Verdict Verdict `json:"verdict"`
}

// ReviewerResult is the Reviewer's whole final call: one verdict per task identifier.
type ReviewerResult struct {
	Verdicts []TaskVerdict `json:"verdicts"`
}

// ReviewerRole returns the compiled Reviewer role.
func ReviewerRole() Role {
	return newRole("reviewer",
		[]string{"read", "search", "skill", "diff", "git_read", "run", "submit_finding"},
		[]string{"build", "test", "lint"},
		[]string{"R1"},
		[]string{"repo_profile", "group", "tasks", "files", "diff", "lens", "lens_rules", "checks"},
		[]string{"repo_context", "standards", "findings", "callers", "specs", "repo_map"},
		"komodo:agent/reviewer/result/1", []string{"verdicts"}, true)
}

// ParseReviewerResult decodes a Reviewer final call, refusing unknown fields and verdict words.
func ParseReviewerResult(raw []byte) (ReviewerResult, error) {
	var r ReviewerResult
	if err := decodeStrict(raw, &r); err != nil {
		return ReviewerResult{}, fmt.Errorf("decode reviewer result: %w", err)
	}
	for _, v := range r.Verdicts {
		if strings.TrimSpace(v.Task) == "" {
			return ReviewerResult{}, errors.New("a verdict has no task identifier")
		}
		switch v.Verdict {
		case VerdictMet, VerdictNotMet, VerdictBriefIncomplete:
		default:
			return ReviewerResult{}, fmt.Errorf("task %s has unknown verdict %q", v.Task, v.Verdict)
		}
	}
	return r, nil
}

// AcceptResult rejects a result that skips or repeats a task identifier, or names a task outside the group.
func (reviewer) AcceptResult(tasks []string, r ReviewerResult) error {
	got := make([]string, len(r.Verdicts))
	for i, v := range r.Verdicts {
		got[i] = v.Task
	}
	if err := ExactlyOnce(tasks, got); err != nil {
		return err
	}
	if len(got) != len(tasks) {
		return errors.New("result names a task that is not in the group")
	}
	return nil
}

// ExactlyOnce rejects a list that skips or repeats a task identifier.
func ExactlyOnce(want, got []string) error {
	seen := map[string]int{}
	for _, g := range got {
		seen[g]++
	}
	for _, w := range want {
		switch seen[w] {
		case 0:
			return fmt.Errorf("task %s has no entry", w)
		case 1:
		default:
			return fmt.Errorf("task %s has %d entries", w, seen[w])
		}
	}
	return nil
}
