package agent

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// BuilderStatus is how a Builder session ended its work.
type BuilderStatus string

const (
	BuilderDone       BuilderStatus = "done"
	BuilderNeedsScope BuilderStatus = "needs_scope"
)

// BuilderResult is the Builder's final call.
type BuilderResult struct {
	Status BuilderStatus `json:"status" desc:"done when the work is finished, needs_scope when it needs more paths."`
	// what changed, in the Builder's words
	Summary string `json:"summary" desc:"What changed, in a few plain sentences."`
	// needs_scope only: the paths asked for
	Paths []string `json:"paths,omitempty" desc:"Only with needs_scope: the paths the work needs."`
	// needs_scope only: why they are needed
	Reason string `json:"reason,omitempty" desc:"Only with needs_scope: why those paths are needed."`
	// file:line of each finding the Builder rejects
	Disputed []string `json:"disputed,omitempty" desc:"The file:line of each review finding you reject as wrong."`
	// done only: the commands the Builder ran to prove its work
	Evidence []EvidenceRun `json:"evidence,omitempty" desc:"Required with done: the commands you ran to prove your work, such as its tests."`
}

// checkBuilderEvidence refuses a done result with no evidence run, a run with no command or an excerpt over 4 KB.
func checkBuilderEvidence(runs []EvidenceRun) error {
	if len(runs) == 0 {
		return errors.New("builder result with status done needs evidence: the commands you ran to prove your work")
	}
	for _, e := range runs {
		if strings.TrimSpace(e.Cmd) == "" {
			return errors.New("builder evidence has a run with no command")
		}
		if len(e.Excerpt) > GateOutputCap {
			return fmt.Errorf("builder evidence has an excerpt over %d bytes; keep the lines that show the result", GateOutputCap)
		}
	}
	return nil
}

// disputedRef matches a finding reference of the form file:line.
var disputedRef = regexp.MustCompile(`^.+:[1-9][0-9]*$`)

// BuilderRole returns the compiled Builder role.
func BuilderRole() Role {
	return newRole("builder",
		[]string{"read", "search", "edit", "write", "run", "skill", "task_done"},
		[]string{"build", "test", "lint", "format"},
		[]string{"R1", "B1"},
		[]string{"repo_profile", "group", "tasks", "files", "file_contents"},
		[]string{"repo_context", "standards", "diff", "checks", "findings", "scope_answer", "unfinished", "specs"},
		"komodo:agent/builder/result/1", BuilderResult{}, true)
}

// ParseBuilderResult decodes and validates a Builder final call.
func ParseBuilderResult(raw []byte) (BuilderResult, error) {
	var r BuilderResult
	if err := core.DecodeStrict(raw, &r); err != nil {
		return BuilderResult{}, fmt.Errorf("decode builder result: %w", err)
	}
	if strings.TrimSpace(r.Summary) == "" {
		return BuilderResult{}, errors.New("builder result has no summary")
	}

	switch r.Status {
	case BuilderDone:
		if len(r.Paths) > 0 || r.Reason != "" {
			return BuilderResult{}, errors.New("builder result with status done must not carry paths or a reason")
		}
		if err := checkBuilderEvidence(r.Evidence); err != nil {
			return BuilderResult{}, err
		}
	case BuilderNeedsScope:
		if len(r.Paths) == 0 || strings.TrimSpace(r.Reason) == "" {
			return BuilderResult{}, errors.New("builder result with status needs_scope needs paths and a reason")
		}
		for _, p := range r.Paths {
			if strings.TrimSpace(p) == "" {
				return BuilderResult{}, errors.New("builder result names an empty path")
			}
		}
	default:
		return BuilderResult{}, fmt.Errorf("builder result has unknown status %q", r.Status)
	}

	for _, d := range r.Disputed {
		if !disputedRef.MatchString(d) {
			return BuilderResult{}, fmt.Errorf("disputed entry %q is not file:line", d)
		}
	}

	return r, nil
}
