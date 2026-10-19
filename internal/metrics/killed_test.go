package metrics

import (
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// TestKilledSessionUsageSums checks a session that ended by timeout still adds its usage to the group and run.
func TestKilledSessionUsageSums(t *testing.T) {
	f := newFigures()
	f.apply(startRun(0, "r", "g"))

	evs := session(0, "r", "g", "s1", "builder", &bus.SessionUsage{TokensIn: 7, TokensOut: 50})
	evs[1].Data.(*bus.SessionEnd).Reason = "timeout"

	var rec *SessionRecord
	for _, e := range evs {
		if r := f.apply(e); r != nil {
			rec = r
		}
	}
	if rec == nil || rec.Reason != "timeout" || rec.Usage == nil || rec.Usage.TokensOut != 50 {
		t.Fatalf("record %+v, want the killed session's usage", rec)
	}

	g, _ := f.group("r", "g")
	if g.Usage.TokensIn != 7 || g.Usage.TokensOut != 50 {
		t.Fatalf("group usage %+v, want 7 in and 50 out", g.Usage)
	}
	if r, _ := f.run("r"); r.Usage.TokensOut != 50 {
		t.Fatalf("run usage %+v, want 50 out", r.Usage)
	}
}
