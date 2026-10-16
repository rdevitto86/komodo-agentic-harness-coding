package task

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCommandsRunWithTheSecretFilesMasked(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	if err := os.WriteFile(filepath.Join(r.wt, ".env"), []byte("KEY=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.instance(context.Background(), "tester"); err != nil {
		t.Fatal(err)
	}
	if exit, _, err := r.m.runner("tester")(context.Background(), "test"); err != nil || exit != 0 {
		t.Fatalf("run: %d, %v", exit, err)
	}
	r.be.mu.Lock()
	defer r.be.mu.Unlock()
	if len(r.be.masked) != 1 || filepath.Base(r.be.masked[0]) != ".env" {
		t.Fatalf("masked %v", r.be.masked)
	}
}
