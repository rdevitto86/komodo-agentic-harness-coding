package task

import (
	"errors"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
)

func TestQACrashesNeverSpendARetry(t *testing.T) {
	r := newPipeRig(t, rigOpts{limits: Limits{GroupRetries: 2}})
	crash := func(llm.Session) (llm.Stream, error) { return nil, errors.New("the process died") }
	r.model.on("builder", building(r.wt, "a.go", "package a\n", builderDone))
	r.model.on("qa", crash, crash, crash, crash, sayResult(qaPass))
	r.model.on("reviewer", sayResult(reviewerMet))
	r.reachCleanup()
	if got := r.rt.progress().RetriesLeft; got != 2 || r.model.started("qa") != 5 {
		t.Fatalf("retries left %d, sessions %v", got, r.model.starts)
	}
	if escs := r.rt.escalations(); len(escs) != 0 {
		t.Fatalf("escalations %+v", escs)
	}
}
