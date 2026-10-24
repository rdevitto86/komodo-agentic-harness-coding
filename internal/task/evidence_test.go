package task

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// The builder's evidence reaches Quality Assurance's brief and the session.end fact; Go does not run it.
func TestBuilderEvidenceReachesQAAndTheEvents(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	r.model.on("builder", building(r.wt, "a.go", "package a\n", builderDone))
	r.model.on("qa", sayResult(qaPass))
	r.model.on("reviewer", sayResult(reviewerMet))
	r.reachCleanup()

	if got := r.slot("qa", 0, "builder_evidence"); !strings.Contains(got, "go test ./a/...") ||
		!strings.Contains(got, "ok  a 0.01s") {
		t.Fatalf("Quality Assurance's brief lacks the builder's evidence: %q", got)
	}

	r.rt.mu.Lock()
	defer r.rt.mu.Unlock()

	seen := false

	for _, f := range r.rt.facts {
		if f.Subtype != "session.end" || !strings.Contains(string(f.Data), `"agent":"builder"`) {
			continue
		}

		var d struct {
			Evidence []struct {
				Cmd string `json:"cmd"`
			} `json:"evidence"`
		}
		if err := json.Unmarshal(f.Data, &d); err != nil {
			t.Fatal(err)
		}
		if len(d.Evidence) == 1 && d.Evidence[0].Cmd == "go test ./a/..." {
			seen = true
		}
	}
	if !seen {
		t.Error("no builder session.end fact carries the evidence")
	}
}

// A done result without evidence goes back to the builder as a gate error.
func TestBuilderJudgeRefusesADoneResultWithoutEvidence(t *testing.T) {
	b := testRoles(t)["builder"]

	var ge *GateError
	if _, err := b.Judge(context.Background(), result(`{"status":"done","summary":"s"}`)); !errors.As(err, &ge) {
		t.Fatalf("a done result without evidence passed: %v", err)
	}
}
