package metrics

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// TestScorecardMatchesHandCount checks first-pass rate, returns per group, finding precision, Quality Assurance false fails,
// cache share and per-group cost against hand-counted values.
func TestScorecardMatchesHandCount(t *testing.T) {
	common := t.TempDir()
	dir := RepoDir(common)
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	at := func(sec int, e bus.Event) string {
		e.Run = "run-a"
		e.Time = base.Add(time.Duration(sec) * time.Second)
		return jsonLine(t, e)
	}
	task := func(sub bus.Subtype, group, session bus.ID, id string) bus.Event {
		g := bus.GroupFacts{Session: session}
		if sub == "task.retry" {
			return bus.Event{
				Type:    bus.TypeRunStatus,
				Subtype: sub,
				Group:   group,
				Data:    &bus.TaskRetry{GroupFacts: g, Task: id, Attempt: 2},
			}
		}
		return bus.Event{Type: bus.TypeRunStatus, Subtype: sub, Group: group, Data: &bus.TaskDone{GroupFacts: g, Task: id}}
	}
	finding := bus.FindingSubmit{GroupFacts: bus.GroupFacts{Session: "r1"}, Rule: "x", File: "a.go", Line: 1, Quote: "q"}
	u1 := &bus.SessionUsage{TokensIn: 100, TokensOut: 10, CacheRead: 50, USDEquiv: 0.5}
	u2 := &bus.SessionUsage{TokensIn: 100, TokensOut: 20, CacheRead: 30, USDEquiv: 0.3}
	end := func(group, session bus.ID, agent string, u *bus.SessionUsage, v *bus.VerifyOutcome) bus.Event {
		return bus.Event{Type: bus.TypeRunStatus, Subtype: "session.end", Group: group,
			Data: &bus.SessionEnd{
				GroupFacts: bus.GroupFacts{Session: session, Reason: "done"},
				Agent:      agent,
				Usage:      u,
				Verify:     v,
			}}
	}
	ls := []string{
		at(0, bus.Event{
			Type:    bus.TypeRunStatus,
			Subtype: "run.start",
			Data:    &bus.RunStartedFact{Groups: []bus.ID{"G1", "G2"}},
		}),
		at(1, task("task.retry", "G1", "b1", "t1")),
		at(2, task("task.done", "G1", "b1", "t1")),
		at(3, task("task.done", "G1", "b1", "t2")),
		at(4, task("task.done", "G2", "b2", "t3")),
		at(5, task("task.done", "G2", "b2", "t4")),
		at(6, end("G1", "b1", "builder", u1, nil)),
		at(7, end("G2", "b2", "builder", u2, nil)),
		at(8, bus.Event{
			Type:    bus.TypeRunStatus,
			Subtype: "stage.enter",
			Group:   "G1",
			Data:    &bus.StageEnter{To: "build", Return: true},
		}),
	}
	for range 4 {
		ls = append(ls, at(9, bus.Event{Type: bus.TypeRunStatus, Subtype: "finding.submit", Group: "G1", Data: &finding}))
	}
	for range 3 {
		ls = append(ls, at(10, bus.Event{Type: bus.TypeRunStatus, Subtype: "finding.verified", Group: "G1",
			Data: &bus.FindingVerified{FindingSubmit: finding, Severity: "major", In: "head"}}))
	}
	ls = append(ls,
		at(11, end("G1", "tt1", "qa", nil, &bus.VerifyOutcome{Failed: []string{"a"}, Disputed: []string{"b"}})),
		at(12, bus.Event{Type: bus.TypeRunComplete, Subtype: "done", Data: &bus.RunDone{Done: []bus.ID{"G1", "G2"}}}),
	)
	writeLog(t, bus.EventsDir(common), "run-a", joinLines(ls...))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var recs []string
	for _, r := range []SessionRecord{
		{Session: "b1", Run: "run-a", Group: "G1", Agent: "builder", Model: "m1", StartedAt: base, Usage: u1},
		{Session: "b2", Run: "run-a", Group: "G2", Agent: "builder", Model: "m1", StartedAt: base, Usage: u2},
		{Session: "r1", Run: "run-a", Group: "G1", Agent: "reviewer", Model: "m2", StartedAt: base},
		{Session: "tt1", Run: "run-a", Group: "G1", Agent: "qa", Model: "m3", StartedAt: base},
	} {
		b, _ := json.Marshal(r)
		recs = append(recs, string(b))
	}
	if err := os.WriteFile(filepath.Join(dir, "sessions.jsonl"), []byte(joinLines(recs...)), 0o644); err != nil {
		t.Fatal(err)
	}

	sc, err := NewReader(dir).Scorecard(context.Background(), 30*24*time.Hour)
	if err != nil || len(sc.Rows) != 3 {
		t.Fatalf("Scorecard = %+v, %v", sc, err)
	}
	b, tt, r := sc.Rows[0], sc.Rows[1], sc.Rows[2]
	if b.Agent != "builder" || b.Model != "m1" || !near(b.FirstPassRate, 0.75) || !near(b.ReturnsPerGroup, 0.5) ||
		!near(b.CacheReadShare, 0.4) {
		t.Errorf("builder row = %+v", b)
	}
	if b.TokensPerGroup == nil || *b.TokensPerGroup != 115 || b.USDPerGroup == nil || !near(*b.USDPerGroup, 0.4) {
		t.Errorf("builder cost = %v %v", b.TokensPerGroup, b.USDPerGroup)
	}
	if r.Agent != "reviewer" || !near(r.FindingPrecision, 0.75) || r.TokensPerGroup != nil || r.USDPerGroup != nil {
		t.Errorf("reviewer row = %+v", r)
	}
	if tt.Agent != "qa" || !near(tt.QAFalseFail, 0.5) || tt.TokensPerGroup != nil {
		t.Errorf("qa row = %+v", tt)
	}
}

// near reports whether two floats agree to four places.
func near(a, b float64) bool { return math.Abs(a-b) < 1e-4 }
