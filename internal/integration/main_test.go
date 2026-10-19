//go:build !windows

package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// TestMain runs the suite, then fails it when an engine started from the suite's binary is still alive, and removes the
// binary's folder.
func TestMain(m *testing.M) {
	code := m.Run()
	if binPath != "" {
		if left := liveEngines(binPath); len(left) > 0 {
			fmt.Fprintf(os.Stderr, "FAIL: %d engine(s) started by the tests outlived them: pids %v\n", len(left), left)
			for _, pid := range left {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
			code = 1
		}
		_ = os.RemoveAll(filepath.Dir(binPath))
	}
	os.Exit(code)
}

// liveEngines returns the process ids of every running "internal engine" whose program is bin.
func liveEngines(bin string) []int {
	out, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return nil
	}
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || !strings.Contains(line, bin+" internal engine") {
			continue
		}
		if pid, err := strconv.Atoi(f[0]); err == nil && pid != os.Getpid() {
			pids = append(pids, pid)
		}
	}
	return pids
}
