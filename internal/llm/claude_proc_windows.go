//go:build windows

package llm

import (
	"os"
	"os/exec"
)

// setProcAttrs does nothing on Windows; the program is stopped by killing its process.
func setProcAttrs(*exec.Cmd) {}

// signalProc ends the process; Windows has no gentler rung that reaches a program without a console.
func signalProc(p *os.Process, _ StopLevel) error { return p.Kill() }
