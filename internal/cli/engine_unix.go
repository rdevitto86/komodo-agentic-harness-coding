//go:build unix

package cli

import "syscall"

// detachedAttrs starts the engine in a process group of its own.
func detachedAttrs() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

// killTree kills the process group the engine leads.
func killTree(pid int) error { return syscall.Kill(-pid, syscall.SIGKILL) }
