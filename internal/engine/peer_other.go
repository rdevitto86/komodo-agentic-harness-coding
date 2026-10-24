//go:build !darwin && !linux

package engine

import "net"

// peerHasTerminal refuses on a platform with no peer check; Windows is untested and has no equivalent here.
func peerHasTerminal(net.Conn) (bool, error) { return false, errNoPeerCheck }
