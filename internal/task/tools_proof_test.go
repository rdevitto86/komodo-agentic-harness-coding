package task

import (
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
)

// A proof Go would refuse is sent back to the Reviewer at submit with its reason; a valid proof is filed.
func TestSubmitFindingChecksTheProof(t *testing.T) {
	var got []Submitted

	cfg := agent.ProofConfig{Source: t.TempDir()}
	r := newRig(t, func(e *Env) {
		e.OnFinding = func(s Submitted) { got = append(got, s) }
		e.CheckProof = cfg.Check
	})

	finding := func(run string) map[string]any {
		return map[string]any{
			"rule": "correctness.2", "file": "cmd/main.go", "line": 1, "quote": "package main", "note": "n",
			"proof": map[string]any{"cmd": run, "expect": "--- FAIL: TestX"},
		}
	}

	reply := r.call("submit_finding", finding("go test ./cmd/ -run '^TestX$'"))
	if !strings.HasPrefix(reply.Text, "error:") || !strings.Contains(reply.Text, "shell quoting") || len(got) != 0 {
		t.Fatalf("quoted proof: reply %q findings %+v", reply.Text, got)
	}

	reply = r.call("submit_finding", finding("go test ./cmd/ -run ^TestX$"))
	if reply.Text != "filed correctness.2 at cmd/main.go:1" || len(got) != 1 {
		t.Fatalf("valid proof: reply %q findings %+v", reply.Text, got)
	}
}
