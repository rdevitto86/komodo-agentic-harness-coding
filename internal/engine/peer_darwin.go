//go:build darwin

package engine

import (
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"syscall"
)

// solLocal and localPeerPID are the socket level and the option that give the process id of a unix socket's peer.
const (
	solLocal     = 0
	localPeerPID = 2
)

// peerHasTerminal finds the peer's process id with LOCAL_PEERPID and asks ps whether that process has a controlling tty.
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
		pid  int
		serr error
	)
	read := func(fd uintptr) { pid, serr = syscall.GetsockoptInt(int(fd), solLocal, localPeerPID) }
	if err := raw.Control(read); err != nil {
		return false, fmt.Errorf("read the peer socket: %w", err)
	}
	if serr != nil {
		return false, fmt.Errorf("read the peer process id: %w", serr)
	}

	out, err := exec.Command("/bin/ps", "-o", "tty=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false, fmt.Errorf("look up the peer process %d: %w", pid, err)
	}

	return psHasTerminal(string(out)), nil
}
