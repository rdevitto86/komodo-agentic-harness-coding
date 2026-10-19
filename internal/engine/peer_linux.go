//go:build linux

package engine

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"syscall"
)

// peerHasTerminal finds the peer's process id with SO_PEERCRED and reads that process's controlling tty from /proc.
func peerHasTerminal(c net.Conn) (bool, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return false, errors.New("the connection is not a unix socket")
	}

	raw, err := uc.SyscallConn()
	if err != nil {
		return false, fmt.Errorf("read the peer socket: %w", err)
	}

	var (
		cred *syscall.Ucred
		serr error
	)
	if err := raw.Control(func(fd uintptr) {
		cred, serr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return false, fmt.Errorf("read the peer socket: %w", err)
	}
	if serr != nil {
		return false, fmt.Errorf("read the peer credentials: %w", serr)
	}

	b, err := os.ReadFile("/proc/" + strconv.Itoa(int(cred.Pid)) + "/stat")
	if err != nil {
		return false, fmt.Errorf("look up the peer process %d: %w", cred.Pid, err)
	}

	n, ok := ttyNrOfStat(string(b))
	if !ok {
		return false, fmt.Errorf("the stat of process %d is malformed", cred.Pid)
	}

	return n != 0, nil
}
