//go:build windows

package cli

import (
	"os"
	"syscall"
)

// detachedProcess starts a process without a console.
const detachedProcess = 0x00000008

// detachedAttrs starts the engine in a new process group with no console.
func detachedAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess}
}

// killTree kills the engine's process; Windows has no process group to signal.
func killTree(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}

	return p.Kill()
}
