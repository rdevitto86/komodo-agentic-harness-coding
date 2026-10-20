package task

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
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

func TestTheCheckRunnerFailsLoudlyWhenTheOutputPassesTheLimit(t *testing.T) {
	size := 16<<20 + 1000
	exit, out, err := checkRunner(t)(context.Background(), fmt.Sprintf("head -c %d /dev/zero | tr '\\0' x", size))
	if err == nil {
		t.Fatalf("no error for %d bytes of output (exit %d, %d bytes returned)", size, exit, len(out))
	}
	if out != "" {
		t.Fatalf("a cut text came back with the error: %d bytes", len(out))
	}
	if !strings.Contains(err.Error(), "16777216") || !strings.Contains(err.Error(), ".log") {
		t.Fatalf("the error names neither the limit nor the log: %v", err)
	}
	path := err.Error()[strings.LastIndex(err.Error(), " ")+1:]
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("the log the error names is missing: %v", statErr)
	}
}
