package agent

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// A file the guard masks is not copied into the proof folder, so a proof's command cannot read it.
func TestProofCopyLeavesOutSecretFiles(t *testing.T) {
	src, root := t.TempDir(), t.TempDir()
	writeTree(t, src, map[string]string{"a.go": "package a\n", ".env": "KEY=1\n", "sub/.env": "KEY=2\n"})

	seen := false
	run := func(_ context.Context, command string) (int, string, error) {
		dir := regexp.MustCompile(`^cd '([^']+)' && `).FindStringSubmatch(command)[1]
		seen = true

		if _, err := os.Stat(filepath.Join(dir, "a.go")); err != nil {
			t.Errorf("copy lacks a.go: %v", err)
		}
		for _, p := range []string{".env", "sub/.env"} {
			if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); err == nil {
				t.Errorf("copy holds the secret file %s", p)
			}
		}

		return 1, "--- FAIL: TestX", nil
	}

	pr := NewProofRunner(ProofConfig{
		Run: run, Source: src, Root: root,
		Secrets: func() ([]string, error) {
			return []string{filepath.Join(src, ".env"), filepath.Join(src, "sub", ".env")}, nil
		},
	})
	_, err := pr(context.Background(), Proof{Cmd: "go test ./ -run TestX", Expect: "--- FAIL: TestX"})
	if err != nil || !seen {
		t.Fatalf("err %v ran %v", err, seen)
	}
}
