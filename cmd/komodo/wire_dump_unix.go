//go:build unix

package main

import (
	"os"
	"os/signal"
	"runtime/pprof"
	"syscall"
)

// dumpOnSignal writes every goroutine stack to the engine log when the process gets SIGUSR1.
func (s *stack) dumpOnSignal() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR1)
	s.spawn(func() {
		for range ch {
			_ = pprof.Lookup("goroutine").WriteTo(s.stderr, 1)
		}
	})
}
