package task

import (
	"encoding/json"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// lastStageDone returns the data of the last stage.done fact the rig's runtime received.
func (r *pipeRig) lastStageDone() json.RawMessage {
	r.rt.mu.Lock()
	defer r.rt.mu.Unlock()

	var raw json.RawMessage

	for _, f := range r.rt.facts {
		if f.Subtype == "stage.done" {
			raw = f.Data
		}
	}

	return raw
}

func TestRiskyGroupWaitsBeforePublish(t *testing.T) {
	riskyRig := func(file string) *pipeRig {
		r := newPipeRig(t, rigOpts{})
		r.model.on("builder", building(r.wt, file, "x\n", builderDone))
		r.model.on("qa", sayResult(qaPass))
		r.model.on("reviewer", sayResult(reviewerMet))
		return r
	}

	t.Run("a critical group holds after its checks and lands only when started again", func(t *testing.T) {
		r := riskyRig("Makefile")
		if err := r.run(); err != ErrHeld {
			t.Fatalf("Run returned %v, want the hold", err)
		}
		if n := len(r.cm.published()); n != 0 {
			t.Fatalf("published %d times while waiting for a person", n)
		}
		if got := r.m.Progress().Stage; got != state.StageTestReview {
			t.Fatalf("stage %q, want test_review while held", got)
		}

		var d struct {
			Stage state.Stage
			Risk  string
		}

		raw := r.lastStageDone()
		_ = json.Unmarshal(raw, &d)

		if d.Stage != state.StageTestReview || d.Risk != string(agent.RiskCritical) {
			t.Fatalf("stage.done %s", raw)
		}

		r.reachCleanup()

		if n := len(r.cm.published()); n != 1 {
			t.Fatalf("published %d times after the start, want 1", n)
		}
	})

	t.Run("a medium group lands with no wait", func(t *testing.T) {
		r := riskyRig("a.go")
		r.reachCleanup()

		if n := len(r.cm.published()); n != 1 {
			t.Fatalf("published %d times, want 1", n)
		}
	})

	t.Run("a lost level is classified again from the diff", func(t *testing.T) {
		r := riskyRig("Makefile")
		if err := r.run(); err != ErrHeld {
			t.Fatalf("Run returned %v", err)
		}

		r.m.mu.Lock()
		r.m.risk = agent.RiskDecision{}
		r.m.mu.Unlock()

		lvl, err := r.m.riskLevel(t.Context())
		if err != nil || lvl != agent.RiskCritical {
			t.Fatalf("level %q err %v", lvl, err)
		}
	})
}
