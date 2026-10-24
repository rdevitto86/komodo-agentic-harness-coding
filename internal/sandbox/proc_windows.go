//go:build windows

package sandbox

import (
	"os"
	"syscall"
)

// ownGroup starts a command in a new console process group.
func ownGroup() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// terminate does nothing, because Windows delivers no termination signal to a process.
func terminate(*os.Process) {}

// killGroup does nothing, because Windows has no process groups to signal.
func killGroup(int) {}

// killPID kills one process and reports whether it died.
func killPID(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	return p.Kill() == nil
}

// identify reports no identity, because Windows files carry no device and inode pair this package can read.
func identify(path string) (fileID, bool) { return fileID{}, false }
