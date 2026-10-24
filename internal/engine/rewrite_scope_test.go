package engine

import (
	"errors"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

func TestRewriteReopensOnlyCompleteGroups(t *testing.T) {
	t.Parallel()

	for _, outcome := range []string{"failed", "abandoned", "dropped"} {
		t.Run("a "+outcome+" group is refused and stays done", func(t *testing.T) {
			t.Parallel()

			r := newRig(t, [][]string{{"tg-1"}})
			if err := r.startRun("run-1", "tg-1"); err != nil {
				t.Fatal(err)
			}

			r.waitStage("tg-1", StageDone)
			waitUntil(t, "the first run ended", func() bool { return r.ev.count(bus.TypeRunComplete, "done") == 1 })

			st := r.store.get("g-tg-1")
			st.Outcome = outcome
			r.store.put("g-tg-1", st, 100)
			r.setTasks("tg-1", "t-tg-1-1", "t-tg-1-2")

			err := r.startRun("run-2", "tg-1")

			var ce *codeError
			if !errors.As(err, &ce) || ce.code != CodeGroupFailed || !strings.Contains(ce.detail, "plan it again") {
				t.Fatalf("err %v", err)
			}
			if got := r.store.get("g-tg-1"); got.Stage != StageDone || got.Outcome != outcome || len(got.Tasks) != 1 {
				t.Fatalf("group %q outcome %q with %d tasks", got.Stage, got.Outcome, len(got.Tasks))
			}
			if n := r.tasks.startedCount("g-tg-1"); n != 1 {
				t.Fatalf("Task Managers started %d times", n)
			}
		})
	}
}
