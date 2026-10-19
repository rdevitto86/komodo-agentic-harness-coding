//go:build windows

package main

// dumpOnSignal does nothing, because Windows has no user signal to ask for a goroutine dump.
func (s *stack) dumpOnSignal() {}
