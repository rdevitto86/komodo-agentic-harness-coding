package resource

import (
	"context"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// TestRecordsRoundTrip: windows saved through the State Manager are loaded back by a new Resource Manager, which keeps only the unreset ones.
func TestRecordsRoundTrip(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	mach, err := state.NewMachine(home, "resource")
	if err != nil {
		t.Fatal(err)
	}
	rec := NewRecords(mach)
	reset := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	k := WindowKey{Link: "claude-cli", Window: "five_hour"}
	if err := rec.SaveWindow(ctx, k, WindowRecord{PercentUsed: 83.2, ResetAt: reset, Sessions: 14}); err != nil {
		t.Fatal(err)
	}
	old := WindowKey{Link: "claude-cli", Window: "weekly"}
	if err := rec.SaveWindow(ctx, old, WindowRecord{PercentUsed: 5, ResetAt: time.Now().Add(-time.Hour), Sessions: 1}); err != nil {
		t.Fatal(err)
	}

	got, err := rec.LoadWindows(ctx)
	if err != nil || len(got) != 2 || got[k].Sessions != 14 || got[k].PercentUsed != 83.2 || !got[k].ResetAt.Equal(reset) {
		t.Fatalf("loaded %+v, err %v", got, err)
	}

	r := newRig(defaults)
	r.m.d.State, r.clock = rec, time.Now()
	r.m.load(ctx)
	if w := r.m.st.Windows[k]; w == nil || w.Sessions != 14 || w.PercentUsed != 83.2 || len(r.m.st.Windows) != 1 {
		t.Fatalf("manager windows %+v", r.m.st.Windows)
	}
}
