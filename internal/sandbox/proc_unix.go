//go:build unix

package sandbox

import (
	"os"
	"syscall"
)

// ownGroup puts a command in a process group of its own, so one signal reaches its whole tree.
func ownGroup() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

// terminate asks a process to stop with the termination signal.
func terminate(p *os.Process) { _ = p.Signal(syscall.SIGTERM) }

// killGroup kills the process group whose leader is root.
func killGroup(root int) { _ = syscall.Kill(-root, syscall.SIGKILL) }

// killPID kills one process and reports whether the signal was delivered.
func killPID(pid int) bool { return syscall.Kill(pid, syscall.SIGKILL) == nil }

// identify returns the identity of the file at a path, or false when it is not there.
func identify(path string) (fileID, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return fileID{}, false
	}

	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fileID{}, false
	}

	return fileID{dev: uint64(st.Dev), ino: uint64(st.Ino)}, true
}
