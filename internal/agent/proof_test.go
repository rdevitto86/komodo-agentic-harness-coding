package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// writeTree writes files (path to text) under a root.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, text := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProofRunnerRunsInAThrowawayCopy(t *testing.T) {
	src, root := t.TempDir(), t.TempDir()
	writeTree(t, src, map[string]string{"a.go": "package a\n", ".git/HEAD": "ref", "sub/b.go": "package sub\n"})

	var (
		seen string
		dir  string
	)

	run := func(_ context.Context, command string) (int, string, error) {
		seen = command
		m := regexp.MustCompile(`^cd '([^']+)' && `).FindStringSubmatch(command)
		if m == nil {
			t.Fatalf("command does not start in the copy: %q", command)
		}
		dir = m[1]
		if dir == src || !strings.HasPrefix(dir, root) {
			t.Errorf("ran in %s, want a folder under %s", dir, root)
		}
		for _, p := range []string{"a.go", "sub/b.go", "x_test.go"} {
			if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); err != nil {
				t.Errorf("copy lacks %s: %v", p, err)
			}
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			t.Error("the copy holds .git")
		}
		return 1, "--- FAIL: TestX", nil
	}

	pr := NewProofRunner(ProofConfig{Run: run, Source: src, Root: root})
	res, err := pr(context.Background(), Proof{
		Test:   &ProofTest{File: "x_test.go", Body: "package a\n", Run: "go test ./ -run TestX"},
		Expect: "--- FAIL: TestX",
	})
	if err != nil || !res.Failed || res.Output != "--- FAIL: TestX" {
		t.Fatalf("result %+v %v", res, err)
	}
	if want := "cd '" + dir + "' && 'go' 'test' './' '-run' 'TestX'"; seen != want {
		t.Errorf("command = %q, want %q", seen, want)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("the copy was not discarded")
	}
	if _, err := os.Stat(filepath.Join(src, "x_test.go")); err == nil {
		t.Error("the proof's test file reached the original worktree")
	}
}

func TestProofRunnerRefuses(t *testing.T) {
	src, root := t.TempDir(), t.TempDir()
	writeTree(t, src, map[string]string{"a_test.go": "package a\n"})

	pr := NewProofRunner(ProofConfig{
		Run: func(context.Context, string) (int, string, error) {
			t.Error("a refused proof ran")
			return 0, "", nil
		},
		Source: src,
		Root:   root,
	})

	cases := map[string]Proof{
		"unlisted command":     {Cmd: "rm -rf /", Expect: "x"},
		"shell metacharacters": {Cmd: "go test ./... ; id", Expect: "x"},
		"no expect line":       {Cmd: "go vet ./...", Expect: " "},
		"both command and test": {
			Cmd:    "go vet ./...",
			Test:   &ProofTest{File: "n_test.go", Run: "go vet ./..."},
			Expect: "x",
		},
		"existing test file": {Test: &ProofTest{File: "a_test.go", Body: "x", Run: "go vet ./..."}, Expect: "x"},
		"test file escapes":  {Test: &ProofTest{File: "../n_test.go", Body: "x", Run: "go vet ./..."}, Expect: "x"},
		"absolute test file": {Test: &ProofTest{File: "/tmp/n_test.go", Body: "x", Run: "go vet ./..."}, Expect: "x"},
		"test file in .git":  {Test: &ProofTest{File: ".git/hooks/x", Body: "x", Run: "go vet ./..."}, Expect: "x"},
		"unlisted test run":  {Test: &ProofTest{File: "n_test.go", Body: "x", Run: "sh n_test.go"}, Expect: "x"},
		"huge test file": {
			Test:   &ProofTest{File: "n_test.go", Body: strings.Repeat("x", MaxProofBody+1), Run: "go vet ./..."},
			Expect: "x",
		},
	}
	for name, p := range cases {
		if _, err := pr(context.Background(), p); !errors.Is(err, ErrProofRefused) {
			t.Errorf("%s: error %v, want ErrProofRefused", name, err)
		}
	}

	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Errorf("a refused proof left %d folders behind", len(entries))
	}
}

func TestProofRunnerKeepsTheOutputOfAnOverLimitRun(t *testing.T) {
	src, root := t.TempDir(), t.TempDir()
	writeTree(t, src, map[string]string{"a.go": "package a\n"})

	pr := NewProofRunner(ProofConfig{
		Run: func(context.Context, string) (int, string, error) {
			return 1, "--- FAIL: TestBig", ErrOutputOverLimit
		},
		Source: src,
		Root:   root,
	})

	res, err := pr(context.Background(), Proof{Cmd: "go test ./...", Expect: "--- FAIL: TestBig"})
	if err != nil || !res.Failed || !strings.Contains(res.Output, "TestBig") {
		t.Errorf("over-limit run: %+v %v", res, err)
	}
}

func TestProofRunnerReportsARunThatCannotStart(t *testing.T) {
	src, root := t.TempDir(), t.TempDir()
	writeTree(t, src, map[string]string{"a.go": "package a\n"})

	pr := NewProofRunner(ProofConfig{
		Run: func(context.Context, string) (int, string, error) {
			return -1, "", errors.New("no sandbox instance")
		},
		Source: src,
		Root:   root,
	})

	if _, err := pr(context.Background(), Proof{Cmd: "go test ./...", Expect: "x"}); err == nil ||
		errors.Is(err, ErrProofRefused) {
		t.Errorf("error = %v, want a start failure", err)
	}
}
