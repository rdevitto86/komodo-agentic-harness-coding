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

// startTolerance is the slack allowed between a recorded and a read process start time; ps reports whole seconds.
const startTolerance = 2 * time.Second

var (
	errNoProcess = errors.New("process does not exist")
	errNoPS      = errors.New("ps is not available")
)

// processInit is the time this process loaded the package; it stands in for the start time when ps cannot give it.
var processInit = time.Now().UTC().Truncate(time.Second)

// ProcessStart returns when the process with this id started, as ps reports it.
func ProcessStart(pid int) (time.Time, error) {
	if pid <= 0 {
		return time.Time{}, errNoProcess
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ps", "-o", "lstart=", "-p", fmt.Sprint(pid))
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return time.Time{}, errNoProcess
		}
		return time.Time{}, fmt.Errorf("%w: %v", errNoPS, err)
	}
	text := strings.Join(strings.Fields(string(out)), " ")
	if text == "" {
		return time.Time{}, errNoProcess
	}
	t, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", text, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("read process start %q: %w", text, err)
	}
	return t.UTC(), nil
}

// OSProcessCheck reports whether a process with this id exists and started at the given time.
// Without ps it falls back to asking the process whether it exists, which cannot tell a reused id.
func OSProcessCheck(pid int, start time.Time) bool {
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
	case errors.Is(err, errNoPS):
		p, ferr := os.FindProcess(pid)
		return ferr == nil && p.Signal(syscall.Signal(0)) == nil
	}
	return false
}

// selfStart returns the start time of this process.
func selfStart() time.Time {
	if t, err := ProcessStart(os.Getpid()); err == nil {
		return t
	}
	return processInit
}
