// Command poc serves the dashboard proof of concept on mock data that moves: go run ./dashboard/poc
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/dashboard"
)

// main copies the mock groups view to a temp file, advances it every 2 seconds and serves it.
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run starts the stage simulator and the server; KOMODO_POC_USAGE=on shows tokens and cost.
func run() error {
	data, err := os.ReadFile("dashboard/testdata/groups.json")
	if err != nil {
		return fmt.Errorf("read mock groups: %w", err)
	}

	var gs []dashboard.Group
	if err := json.Unmarshal(data, &gs); err != nil {
		return fmt.Errorf("decode mock groups: %w", err)
	}

	views := filepath.Join(os.TempDir(), "komodo-dashboard-groups.json")
	if err := write(views, gs); err != nil {
		return err
	}

	go simulate(views, gs)

	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Errorf("make token: %w", err)
	}

	usage := os.Getenv("KOMODO_POC_USAGE") == "on"
	return dashboard.Serve(dashboard.New(hex.EncodeToString(b), views, usage))
}

// simulate moves every group one stage forward every 2 seconds, wrapping after Ship.
func simulate(views string, gs []dashboard.Group) {
	pipeline := dashboard.Stages[:5]
	for range time.Tick(2 * time.Second) {
		for i := range gs {
			next := 0
			for j, st := range pipeline {
				if st == gs[i].Stage {
					next = (j + 1) % len(pipeline)
				}
			}
			gs[i].Stage, gs[i].Block = pipeline[next], ""
		}
		_ = write(views, gs)
	}
}

// write saves the groups view as JSON.
func write(path string, gs []dashboard.Group) error {
	data, err := json.Marshal(gs)
	if err != nil {
		return fmt.Errorf("encode groups: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write groups view: %w", err)
	}
	return nil
}
