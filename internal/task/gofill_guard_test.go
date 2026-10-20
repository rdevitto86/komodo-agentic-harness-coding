package task

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
)

// setupBlock returns the detail of the setup block err carries.
func setupBlock(t *testing.T, err error) map[string]string {
	t.Helper()

	var be *blockErr
	if !errors.As(err, &be) || be.reason != "setup" {
		t.Fatalf("error %v, want a setup block", err)
	}

	d, _ := be.detail.(map[string]string)

	return d
}

func TestTheHostFillLeavesGoModAndGoSumAsItFoundThem(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sum     string // go.sum before the fill; empty means no file
		fillErr error
	}{
		{"go.sum present, fill succeeds", "github.com/kr/text v0.1.0 h1:old\n", nil},
		{"go.sum present, fill fails", "github.com/kr/text v0.1.0 h1:old\n", errors.New("go mod download: boom")},
		{"no go.sum, fill succeeds", "", nil},
		{"no go.sum, fill fails", "", errors.New("go mod download: boom")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, calls := fillRig(t, true, true, nil)

			sumPath := filepath.Join(r.wt, "go.sum")
			if tc.sum != "" {
				if err := os.WriteFile(sumPath, []byte(tc.sum), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			var modfile string

			hostGoDownload = func(_ context.Context, dir string, env []string) error {
				*calls = append(*calls, fillCall{dir: dir})
				modfile = modfileOf(env)
				if dir != r.wt || modfile == "" || strings.HasPrefix(modfile, r.wt) {
					t.Errorf("fill in %q with modfile %q, want the worktree and a copy outside it", dir, modfile)
				}
				if mod, _ := os.ReadFile(modfile); string(mod) != "module x\n" {
					t.Errorf("the modfile copy holds %q", mod)
				}
				if got, _ := os.ReadFile(strings.TrimSuffix(modfile, ".mod") + ".sum"); string(got) != tc.sum {
					t.Errorf("the go.sum copy holds %q, want %q", got, tc.sum)
				}

				sum := strings.TrimSuffix(modfile, ".mod") + ".sum"
				if err := os.WriteFile(sum, []byte("written by the fill\n"), 0o644); err != nil {
					return err
				}
				if err := os.WriteFile(modfile, []byte("module changed\n"), 0o644); err != nil {
					return err
				}

				return tc.fillErr
			}

			_, err := r.m.instance(context.Background(), "builder")
			if (err != nil) != (tc.fillErr != nil) {
				t.Fatalf("instance returned %v, fill error %v", err, tc.fillErr)
			}
			if _, statErr := os.Stat(filepath.Dir(modfile)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("the private copy %q was not removed: %v", filepath.Dir(modfile), statErr)
			}

			got, readErr := os.ReadFile(sumPath)
			switch {
			case tc.sum == "" && !errors.Is(readErr, os.ErrNotExist):
				t.Fatalf("go.sum exists after the fill: %q, %v", got, readErr)
			case tc.sum != "" && string(got) != tc.sum:
				t.Fatalf("go.sum is %q after the fill, want %q", got, tc.sum)
			}
			if mod, _ := os.ReadFile(filepath.Join(r.wt, "go.mod")); string(mod) != "module x\n" {
				t.Fatalf("go.mod is %q after the fill", mod)
			}
		})
	}
}

// modfileOf returns the -modfile path in the fill's GOFLAGS, or an empty string.
func modfileOf(env []string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if flags, ok := strings.CutPrefix(env[i], "GOFLAGS="); ok {
			for _, f := range strings.Fields(flags) {
				if path, ok := strings.CutPrefix(f, "-modfile="); ok {
					return path
				}
			}

			return ""
		}
	}

	return ""
}

func TestAnEditMadeInTheWorktreeWhileTheFillRunsIsKept(t *testing.T) {
	r, _ := fillRig(t, true, true, nil)

	sumPath := filepath.Join(r.wt, "go.sum")
	hostGoDownload = func(context.Context, string, []string) error {
		return os.WriteFile(sumPath, []byte("a builder's line\n"), 0o644)
	}

	if _, err := r.m.instance(context.Background(), "tester"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(sumPath); string(got) != "a builder's line\n" {
		t.Fatalf("go.sum is %q, want the edit kept", got)
	}
}

func TestAFillThatWroteGoSumDoesNotRunAgainForTheNextRole(t *testing.T) {
	r, calls := fillRig(t, true, true, nil)

	hostGoDownload = func(_ context.Context, dir string, env []string) error {
		*calls = append(*calls, fillCall{dir: dir})
		return os.WriteFile(strings.TrimSuffix(modfileOf(env), ".mod")+".sum", []byte("written by the fill\n"), 0o644)
	}

	for _, role := range []string{"builder", "tester"} {
		if _, err := r.m.instance(context.Background(), role); err != nil {
			t.Fatal(err)
		}
	}

	if len(*calls) != 1 {
		t.Fatalf("%d host downloads for two roles, want 1", len(*calls))
	}
}

func TestAFailedHostFillPutsAtMost4KBInTheSetupBlock(t *testing.T) {
	r, _ := fillRig(t, true, true, errors.New("go mod download: "+strings.Repeat("x", 20<<10)))

	_, err := r.m.instance(context.Background(), "builder")
	if out := setupBlock(t, err)["output"]; len(out) == 0 || len(out) > agent.GateOutputCap {
		t.Fatalf("the setup block holds %d bytes of output, want 1 to %d", len(out), agent.GateOutputCap)
	}
}

func TestAHostFillThatTimesOutIsASetupBlockWithTheFixLine(t *testing.T) {
	r, _ := fillRig(t, true, true, fmt.Errorf("go mod download: %w", context.DeadlineExceeded))

	_, err := r.m.instance(context.Background(), "builder")

	d := setupBlock(t, err)
	if !strings.Contains(d["output"], "deadline exceeded") || !strings.Contains(d["fix"], "komodo run tg-1") {
		t.Fatalf("detail %v lacks the timeout or the fix line", d)
	}
}

func TestAHostCommandIsKilledWhenItsOwnTimeoutRunsOut(t *testing.T) {
	start := time.Now()

	_, err := hostOutput(context.Background(), t.TempDir(), os.Environ(), "sleeper", 100*time.Millisecond, "sleep", "30")
	if err == nil || !strings.HasPrefix(err.Error(), "sleeper: ") {
		t.Fatalf("error %v, want a failure labelled sleeper", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("the command ran %v, past its 100ms timeout", time.Since(start))
	}
	if goFillTimeout != 10*time.Minute {
		t.Fatalf("the fill timeout is %v, want 10m", goFillTimeout)
	}
}

func TestTwoRolesFillingAtOnceDownloadOnce(t *testing.T) {
	r, _ := fillRig(t, true, true, nil)

	var n atomic.Int32

	hostGoDownload = func(_ context.Context, _ string, env []string) error {
		n.Add(1)
		time.Sleep(50 * time.Millisecond)

		return os.WriteFile(strings.TrimSuffix(modfileOf(env), ".mod")+".sum", []byte("written by the fill\n"), 0o644)
	}

	var wg sync.WaitGroup
	for _, role := range []string{"builder", "tester"} {
		wg.Add(1)

		go func() {
			defer wg.Done()

			if _, err := r.m.instance(context.Background(), role); err != nil {
				t.Error(err)
			}
		}()
	}

	wg.Wait()

	if n.Load() != 1 {
		t.Fatalf("%d host downloads for two roles at once, want 1", n.Load())
	}
}
