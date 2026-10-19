package engine

import (
	"errors"
	"net"
	"strconv"
	"strings"
)

// PeerChecker reports whether the process on the other end of a control-socket connection has a controlling terminal.
type PeerChecker func(c net.Conn) (bool, error)

// errNoPeerCheck marks a platform where the peer of a socket cannot be checked.
var errNoPeerCheck = errors.New("the peer's terminal cannot be checked on this platform")

// ttyNrOfStat returns the controlling terminal number from the text of a /proc/<pid>/stat file; false when it is malformed.
func ttyNrOfStat(stat string) (int, bool) {
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return 0, false
	}

	// after the command name: state, ppid, pgrp, session, tty_nr
	f := strings.Fields(stat[end+1:])
	if len(f) < 5 {
		return 0, false
	}

	n, err := strconv.Atoi(f[4])
	return n, err == nil
}

// psHasTerminal reports whether the tty column that ps printed for a process names a terminal.
func psHasTerminal(out string) bool {
	tty := strings.TrimSpace(out)
	return tty != "" && !strings.HasPrefix(tty, "?")
}
