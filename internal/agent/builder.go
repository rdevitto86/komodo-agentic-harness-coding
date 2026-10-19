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
	Status   BuilderStatus `json:"status"`
	Summary  string        `json:"summary"`            // what changed, in the Builder's words
	Paths    []string      `json:"paths,omitempty"`    // needs_scope only: the paths asked for
	Reason   string        `json:"reason,omitempty"`   // needs_scope only: why they are needed
	Disputed []string      `json:"disputed,omitempty"` // file:line of each finding the Builder rejects
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
		"komodo:agent/builder/result/1", []string{"status", "summary"}, true)
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
