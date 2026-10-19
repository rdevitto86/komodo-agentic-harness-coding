//go:build windows

package task

import (
	"os/exec"
	"syscall"
	"time"
)

// reapGroup does nothing, because Windows has no process group to sweep.
func reapGroup(*exec.Cmd) {}

// killGroupOnCancel starts the command in a new process group and makes a cancel kill the command's process.
func killGroupOnCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = 5 * time.Second
}
