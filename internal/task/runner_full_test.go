package task

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
)

// checkRunner returns the runner the checks get, bound to a started tester instance.
func checkRunner(t *testing.T) func(context.Context, string) (int, string, error) {
	t.Helper()
	r := newPipeRig(t, rigOpts{})
	if _, err := r.m.instance(context.Background(), "tester"); err != nil {
		t.Fatal(err)
	}

	return r.m.Runner("tester")
}

func TestTheCheckRunnerReturnsTheWholeOutputOfALongCommand(t *testing.T) {
	var want strings.Builder
	for i := 1; i <= 25000; i++ {
		fmt.Fprintf(&want, "line %07d\n", i)
	}

	exit, out, err := checkRunner(t)(context.Background(), "seq -f 'line %07g' 1 25000")
	if err != nil || exit != 0 {
		t.Fatalf("run: %d, %v", exit, err)
	}
	if out != want.String() {
		t.Fatalf("output is %d bytes, want %d; it starts %q", len(out), want.Len(), out[:min(len(out), 80)])
	}
}

// bigOutput is a shell command that prints size bytes of x and exits with code.
func bigOutput(size, code int) string {
	return fmt.Sprintf("head -c %d /dev/zero | tr '\\0' x; exit %d", size, code)
}

func TestTheCheckRunnerReportsTheExitCodeTheCutTextAndTheLogWhenTheOutputPassesTheLimit(t *testing.T) {
	size := checkOutputCap + 1000
	exit, out, err := checkRunner(t)(context.Background(), bigOutput(size, 3))
	if !errors.Is(err, agent.ErrOutputOverLimit) {
		t.Fatalf("error %v for %d bytes of output, want the over-limit error", err, size)
	}
	if exit != 3 {
		t.Fatalf("exit %d, want 3", exit)
	}
	if len(out) > replyCap || !strings.HasPrefix(out, "xxx") || !strings.HasSuffix(out, "xxx") {
		t.Fatalf("the cut text is %d bytes and not a head and a tail of the output", len(out))
	}
	if !strings.Contains(err.Error(), "16777216") || !strings.Contains(err.Error(), ".log") {
		t.Fatalf("the error names neither the limit nor the log: %v", err)
	}

	path := err.Error()[strings.LastIndex(err.Error(), " ")+1:]
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("the log the error names is missing: %v", statErr)
	}
}

func TestTheCheckRunnerReportsTheLimitEvenWhenTheLogCannotBeKept(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	if _, err := r.m.instance(context.Background(), "tester"); err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(t.TempDir(), "not-a-folder")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	r.m.c.SessionsDir = file

	exit, out, err := r.m.Runner("tester")(context.Background(), bigOutput(checkOutputCap+1000, 0))
	if !errors.Is(err, agent.ErrOutputOverLimit) || exit != 0 {
		t.Fatalf("exit %d, error %v, want 0 and the over-limit error", exit, err)
	}
	if len(out) > replyCap {
		t.Fatalf("the cut text is %d bytes", len(out))
	}
}

func TestACutReplyIsBuiltFromTheHeadAndTailOnly(t *testing.T) {
	cmd := exec.Command("sh", "-c", bigOutput(2<<20, 0))
	res := runConfinedWithin(context.Background(), cmd, filepath.Join(t.TempDir(), "o.log"), 1<<20)

	if !res.Over || res.Reply.LogPath == "" {
		t.Fatalf("over %v, log %q", res.Over, res.Reply.LogPath)
	}
	if len(res.Reply.Text) > replyCap {
		t.Fatalf("the reply holds %d bytes, want at most %d", len(res.Reply.Text), replyCap)
	}
}

func TestTheTaskGateUsesTheExitCodeOfACommandWhoseOutputPassesTheLimit(t *testing.T) {
	for _, tc := range []struct {
		code, exit int
		done       bool
	}{{0, 0, true}, {3, 3, false}} {
		r := newPipeRig(t, rigOpts{mut: func(s *GroupSpec, _ *Collaborators) {
			s.Tasks[0].DoneWhen = bigOutput(checkOutputCap+1000, tc.code)
		}})
		if _, err := r.m.instance(context.Background(), "builder"); err != nil {
			t.Fatal(err)
		}

		exit, out, err := r.m.finishTask(context.Background(), "t1")
		if err != nil || exit != tc.exit {
			t.Fatalf("code %d: finishTask returned %d, %v", tc.code, exit, err)
		}
		if len(out) > agent.GateOutputCap {
			t.Fatalf("code %d: %d bytes of output", tc.code, len(out))
		}
		if done := slices.Contains(r.m.Progress().TasksDone, "t1"); done != tc.done {
			t.Fatalf("code %d: task done %v, want %v", tc.code, done, tc.done)
		}
	}
}
