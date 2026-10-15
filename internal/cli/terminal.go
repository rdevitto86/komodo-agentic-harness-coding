package cli

import "os"

// HasControllingTerminal reports whether /dev/tty opens; a failed open is the no-terminal result an agent shell gets.
func HasControllingTerminal() bool {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}
