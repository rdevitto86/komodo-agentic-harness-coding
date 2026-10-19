package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEngineThatCannotServePublishesTheFinalErrorEvent(t *testing.T) {
	// A control socket path longer than the operating system allows makes Serve fail.
	home := filepath.Join(t.TempDir(), strings.Repeat("x", 120))
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := runEngine(context.Background(), home, io.Discard); err == nil {
		t.Fatal("runEngine returned no error for an unusable socket path")
	}

	b, err := os.ReadFile(filepath.Join(home, "events", "engine.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"final":true`) {
		t.Fatalf("no error event with final true in the event store:\n%s", b)
	}
}
