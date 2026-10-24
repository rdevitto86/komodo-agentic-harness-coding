package engine

import (
	"sync/atomic"
	"testing"
	"time"
)

// A rewrite that arrives while the finished group is still releasing its lease waits for that group to end, so the
// reopened group starts from the record the old group left, not one the old group is about to replace.
func TestReopenWaitsForTheFinishedGroupToEnd(t *testing.T) {
	var n atomic.Int32

	release, grant := make(chan struct{}), make(chan struct{})
	r := newRig(t, [][]string{{"tg-1"}}, func(r *rig) {
		r.tasks.script = numbered(&n)
		r.leases.releaseGate, r.leases.grantGate = release, grant
	})

	if err := r.startRun("run-1", "tg-1"); err != nil {
		t.Fatal(err)
	}

	r.waitStage("tg-1", StageDone)
	r.setTasks("tg-1", "t-tg-1-1", "t-tg-1-2")

	// The old group ends after the rewrite arrives and before the new group may take its lease.
	time.AfterFunc(100*time.Millisecond, func() { close(release) })
	time.AfterFunc(400*time.Millisecond, func() { close(grant) })

	if err := r.startRun("run-2", "tg-1"); err != nil {
		t.Fatal(err)
	}

	waitUntil(t, "the group done again with both tasks", func() bool {
		s := r.store.get("g-tg-1")
		return s.Stage == StageDone && len(s.Tasks) == 2 && s.Tasks[1].State == "done" && s.Run == "run-2"
	})
}
