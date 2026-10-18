package lease

import (
	"errors"
	"testing"
	"time"
)

func TestLive(t *testing.T) {
	now := testStart.Add(10 * time.Hour)
	start := testStart
	alive := func(pid int, s time.Time) bool { return pid == 7 && s.Equal(start) }
	held := Lease{State: StateHeld, Host: "h1", PID: 7, ProcStart: start, RenewedAt: testStart}
	t.Run("this host, process alive, last renewal 3 hours old", func(t *testing.T) {
		l := held
		l.RenewedAt = now.Add(-3 * time.Hour)
		if !l.Live(now, "h1", alive) {
			t.Fatal("a same-host holder with a live process must stay live however old the renewal")
		}
	})
	t.Run("another host renewed 2 hours and 1 minute ago", func(t *testing.T) {
		l := held
		l.Host, l.RenewedAt = "h2", now.Add(-LeaseExpiry-time.Minute)
		if l.Live(now, "h1", alive) {
			t.Fatal("a lease on another host past LeaseExpiry must be dead")
		}
	})
	t.Run("another host renewed exactly at the expiry", func(t *testing.T) {
		l := held
		l.Host, l.RenewedAt = "h2", now.Add(-LeaseExpiry)
		if !l.Live(now, "h1", alive) {
			t.Fatal("a lease on another host no older than LeaseExpiry must be live")
		}
	})
	t.Run("another host never runs the process check", func(t *testing.T) {
		l := held
		l.Host, l.RenewedAt = "h2", now
		if !l.Live(now, "h1", func(int, time.Time) bool { t.Fatal("process check ran"); return false }) {
			t.Fatal("a young remote lease must be live")
		}
	})
	t.Run("reused process id with another start time", func(t *testing.T) {
		l := held
		l.ProcStart = start.Add(time.Hour)
		if l.Live(now, "h1", alive) {
			t.Fatal("a reused pid is a different process, so the lease is dead")
		}
	})
	t.Run("process missing", func(t *testing.T) {
		l := held
		l.PID = 8
		if l.Live(now, "h1", alive) {
			t.Fatal("a missing process means a dead lease")
		}
	})
	t.Run("revoked is never live", func(t *testing.T) {
		l := held
		l.State = StateRevoked
		if l.Live(now, "h1", alive) {
			t.Fatal("a revoked lease is never live")
		}
	})
	t.Run("no process check means dead", func(t *testing.T) {
		if held.Live(now, "h1", nil) {
			t.Fatal("without a check a same-host holder cannot be shown alive")
		}
	})
}

func TestHeldErrorUnwraps(t *testing.T) {
	err := error(HeldError{Holder: Lease{Run: "run-a"}})
	if !errors.Is(err, ErrLeased) {
		t.Fatal("HeldError must unwrap to ErrLeased")
	}
	if err.Error() != "group is leased by run run-a" {
		t.Fatalf("message %q", err.Error())
	}
}
