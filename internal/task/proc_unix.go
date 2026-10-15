//go:build unix

package task

import (
	"os/exec"
	"syscall"
	"time"
)

// killGroupOnCancel puts the command in its own process group and makes a cancel kill the whole group.
func killGroupOnCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second
}
