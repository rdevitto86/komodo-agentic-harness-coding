//go:build !windows

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A stand-in engine outlives a stand-in test process; the watchdog kills it and removes the binary's folder once that
// process ends.
func TestWatchdogStopsEnginesOfADeadTestRun(t *testing.T) {
	dir, err := os.MkdirTemp("", "kbin")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	bin := filepath.Join(dir, "komodo")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nwhile :; do sleep 1; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	engine := exec.Command(bin, "internal", "engine")
	if err := engine.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Process.Kill() })
	done := make(chan struct{})
	go func() { _ = engine.Wait(); close(done) }()
	owner := exec.Command("sleep", "2")
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	if err := startWatchdog(owner.Process.Pid, bin); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		t.Fatal("the engine ended before its test run did")
	case <-time.After(time.Second):
	}
	_ = owner.Wait()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the engine still runs 10s after its test run ended")
	}
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("the binary's folder %s still exists", dir)
		}
	}
}
