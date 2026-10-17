package lease

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

var (
	errNoProcess = errors.New("process does not exist")
	errPSUnknown = errors.New("ps gave no answer")
)

// runPS runs ps for one process and returns its output; tests replace it to simulate a failing ps.
var runPS = func(ctx context.Context, pid int) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "ps", "-o", "lstart=", "-p", fmt.Sprint(pid))

	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")

	return cmd.Output()
}

// ProcessStart returns when the process with this id started, as ps reports it.
// ps exiting 1 with no output means no such process; a timeout, a kill or any other failure is
// errPSUnknown.
func ProcessStart(pid int) (time.Time, error) {
	if pid <= 0 {
		return time.Time{}, errNoProcess
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := runPS(ctx, pid)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 && len(out) == 0 && len(ee.Stderr) == 0 {
			return time.Time{}, errNoProcess
		}

		return time.Time{}, fmt.Errorf("%w: %v", errPSUnknown, err)
	}

	text := strings.Join(strings.Fields(string(out)), " ")
	if text == "" {
		return time.Time{}, errNoProcess
	}

	t, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", text, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: read process start %q: %v", errPSUnknown, text, err)
	}

	return t.UTC(), nil
}

// OSProcessCheck reports whether a process with this id exists and started at the given time.
// When ps gives no answer it asks the process with signal 0, which cannot tell a reused id but
// never calls a live process dead.
func OSProcessCheck(pid int, start time.Time) bool {
	// startTolerance is the slack allowed between a recorded and a read process start time; ps
	// reports whole seconds.
	const startTolerance = 2 * time.Second

	if pid <= 0 {
		return false
	}

	got, err := ProcessStart(pid)

	switch {
	case err == nil:
		if start.IsZero() {
			return true
		}

		d := got.Sub(start)

		return d >= -startTolerance && d <= startTolerance
	case errors.Is(err, errPSUnknown):
		p, ferr := os.FindProcess(pid)
		if ferr != nil {
			return false
		}

		serr := p.Signal(syscall.Signal(0))

		return serr == nil || errors.Is(serr, syscall.EPERM)
	}

	return false
}

// selfStart returns the start time of this process, or the zero time when ps cannot give it.
func selfStart() time.Time {
	t, err := ProcessStart(os.Getpid())
	if err != nil {
		return time.Time{}
	}

	return t
}
