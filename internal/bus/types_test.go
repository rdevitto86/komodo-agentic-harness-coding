package bus

import (
	"strings"
	"testing"
)

// eventNameRow is one published event name and its pair; none marks a name that is not published.
type eventNameRow struct {
	name string
	key  Key
	none bool
}

var eventNameRows = []eventNameRow{
	{"run.start", Key{TypeRunStatus, "run.start"}, false},
	{"run.done", Key{TypeRunComplete, "done"}, false},
	{"run.stopped", Key{TypeRunComplete, "stopped"}, false},
	{"group.paused", Key{TypeRunStatus, "group.paused"}, false},
	{"group.resumed", Key{TypeRunStatus, "group.resumed"}, false},
	{"group.blocked", Key{TypeRunFailure, "group.blocked"}, false},
	{"lease.lost", Key{TypeRunFailure, "lease.lost"}, false},
	{"stage.enter", Key{TypeRunStatus, "stage.enter"}, false},
	{"stage.exit", Key{TypeRunStatus, "stage.exit"}, false},
	{"session.start", Key{TypeRunStatus, "session.start"}, false},
	{"session.idle", Key{TypeRunStatus, "session.idle"}, false},
	{"session.timewarn", Key{TypeRunStatus, "session.timewarn"}, false},
	{"session.end", Key{TypeRunStatus, "session.end"}, false},
	{"task.claimed", Key{TypeRunStatus, "task.claimed"}, false},
	{"task.released", Key{TypeRunStatus, "task.released"}, false},
	{"task.done", Key{TypeRunStatus, "task.done"}, false},
	{"task.retry", Key{TypeRunStatus, "task.retry"}, false},
	{"check.done", Key{TypeRunStatus, "check.done"}, false},
	{"finding.submit", Key{TypeRunStatus, "finding.submit"}, false},
	{"finding.verified", Key{TypeRunStatus, "finding.verified"}, false},
	{"tool.denied", Key{TypeRunStatus, "tool.denied"}, false},
	{"sandbox.fallback", Key{TypeRunStatus, "sandbox.fallback"}, false},
	{"tool.outside", Key{TypeRunStatus, "tool.outside"}, false},
	{"epic.paused", Key{TypeRunStatus, "epic.paused"}, false},
	{"epic.resumed", Key{TypeRunStatus, "epic.resumed"}, false},
	{"timer.due", Key{TypeRunStatus, "timer.due"}, false},
	{"lock.taken", Key{TypeRunStatus, "lock.taken"}, false},
	{"lock.released", Key{TypeRunStatus, "lock.released"}, false},
	{"action.start", Key{}, true},
	{"action.done", Key{}, true},
	{"state.truncated", Key{}, true},
	{"provider.skip", Key{TypeRunStatus, "provider.skip"}, false},
	{"limit.warn", Key{TypeResource, "limit.warn"}, false},
	{"lease.taken", Key{TypeRunStatus, "lease.taken"}, false},
	{"pool.slot.taken", Key{}, true},
	{"queue.enqueued", Key{}, true},
	{"request.resume", Key{TypeRunContinue, ""}, false},
	{"request.timer", Key{}, true},
	{"request.accepted", Key{TypeRunStatus, "request.accepted"}, false},
	{"request.refused", Key{TypeRunStatus, "request.refused"}, false},
	// The command events and the bus's own event.
	{"run-start", Key{TypeRunStart, ""}, false},
	{"run-stop", Key{TypeRunStop, ""}, false},
	{"bus.overflow", Key{TypeError, "bus.overflow"}, false},
}

// Every row of the event-name table is registered, or marked none.
func TestEventNameTableIsRegistered(t *testing.T) {
	for _, r := range eventNameRows {
		_, ok := lookup(r.key)
		switch {
		case r.none && ok:
			t.Errorf("%s is marked none but %v is registered", r.name, r.key)
		case !r.none && !ok:
			t.Errorf("%s: %v is not registered", r.name, r.key)
		}
	}
}

// Every registered struct reports the key it is registered under, and no built-in key is missing from the table.
func TestRegisteredStructsMatchTheirKey(t *testing.T) {
	inTable := map[Key]bool{}
	for _, r := range eventNameRows {
		if !r.none {
			inTable[r.key] = true
		}
	}
	for _, k := range registeredKeys() {
		newData, _ := lookup(k)
		if got := newData().EventKey(); got != k {
			t.Errorf("registered %v but EventKey is %v", k, got)
		}
		if !inTable[k] && !strings.HasPrefix(string(k.Subtype), "test.") {
			t.Errorf("%v is registered but has no table row", k)
		}
	}
}

// The 12 type constants are the whole list.
func TestTwelveTypes(t *testing.T) {
	if n := len(allTypes); n != 12 {
		t.Fatalf("%d types, want 12", n)
	}
}
