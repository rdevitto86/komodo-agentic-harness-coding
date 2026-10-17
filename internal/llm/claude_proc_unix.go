//go:build !windows

package llm

import (
	"os"
	"os/exec"
	"syscall"
)

// setProcAttrs puts the program in its own process group so one signal reaches its whole tree.
func setProcAttrs(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// signalProc applies one rung of the stop ladder; Kill reaches the whole process group.
func signalProc(p *os.Process, level StopLevel) error {
	switch level {
	case Interrupt:
		return p.Signal(syscall.SIGINT)
	case Terminate:
		return p.Signal(syscall.SIGTERM)
	}

	return syscall.Kill(-p.Pid, syscall.SIGKILL)
}
