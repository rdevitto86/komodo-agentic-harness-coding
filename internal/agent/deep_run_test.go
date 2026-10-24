package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// basics answers the module and package listing of a repository with packages m/a and m/b, where b imports a.
func basics() *sandbox {
	return newSandbox().
		on("go list -m", reply{0, "m\n"}).
		on("go list -test", reply{0, "m/a \nm/b m/a\nm/b.test m/a m/b\n"}).
		on("find . -name go.mod", reply{0, "./go.mod\n"})
}

func newTestDeep(c DeepConfig) Deep {
	if c.Changed == nil {
		c.Changed = func(context.Context) ([]string, error) { return []string{"a/a.go"}, nil }
	}
	if c.Language == "" {
		c.Language = "go"
	}
	return NewDeep(c)
}

// The race detector runs over the impact set, and a race returns the work with the output.
func TestDeepRace(t *testing.T) {
	s := basics().on("-race", reply{66, "WARNING: DATA RACE\n"})
	res, err := newTestDeep(DeepConfig{}).Run(context.Background(), s.run, []DeepKind{DeepRace})
	if err != nil || len(res) != 1 {
		t.Fatalf("res %+v err %v", res, err)
	}
	if res[0].Kind != DeepRace || res[0].Pass || !strings.Contains(res[0].Output, "DATA RACE") {
		t.Errorf("result = %+v", res[0])
	}
	last := s.ran[len(s.ran)-1]
	if !strings.Contains(last, "go test -race") || !strings.Contains(last, "'m/a'") || !strings.Contains(last, "'m/b'") {
		t.Errorf("race command = %q", last)
	}

	s = basics().on("-race", reply{0, "ok\n"})
	res, err = newTestDeep(DeepConfig{}).Run(context.Background(), s.run, []DeepKind{DeepRace})
	if err != nil || !res[0].Pass {
		t.Errorf("clean race = %+v %v", res, err)
	}
}

// A test that fails with no race report is the baseline's to judge, so the race check passes.
func TestDeepRaceIgnoresAFailureWithNoRace(t *testing.T) {
	s := basics().on("-race", reply{1, "--- FAIL: TestAlreadyBroken (0.00s)\n    old_test.go:5: broken at base\nFAIL\n"})
	res, err := newTestDeep(DeepConfig{}).Run(context.Background(), s.run, []DeepKind{DeepRace})
	if err != nil || len(res) != 1 || !res[0].Pass {
		t.Fatalf("a failure with no race = %+v %v", res, err)
	}
}

// Fuzzing runs each existing target of a changed package for 30 seconds, and a crash fails the check.
func TestDeepFuzz(t *testing.T) {
	s := basics().
		on("-list", reply{0, "FuzzParse\nFuzzEncode\nTestOther\nok  \tm/a\t0.01s\n"}).
		on("FuzzEncode", reply{1, "--- FAIL: FuzzEncode\n"})
	res, err := newTestDeep(DeepConfig{}).Run(context.Background(), s.run, []DeepKind{DeepFuzz})
	if err != nil || len(res) != 1 || res[0].Pass || !strings.Contains(res[0].Output, "FuzzEncode") {
		t.Fatalf("res %+v err %v", res, err)
	}

	var fuzzed []string
	for _, c := range s.ran {
		if strings.Contains(c, "-fuzz ") {
			fuzzed = append(fuzzed, c)
			if !strings.Contains(c, "-fuzztime 30s") || !strings.Contains(c, "-run '^$'") {
				t.Errorf("fuzz command = %q", c)
			}
		}
	}
	if len(fuzzed) != 2 {
		t.Errorf("fuzzed %v", fuzzed)
	}
}

// A changed package with no fuzz target passes, and runs no fuzz command.
func TestDeepFuzzNoTargets(t *testing.T) {
	s := basics().on("-list", reply{0, "TestOther\nok  \tm/a\t0.01s\n"})
	res, err := newTestDeep(DeepConfig{}).Run(context.Background(), s.run, []DeepKind{DeepFuzz})
	if err != nil || !res[0].Pass {
		t.Fatalf("res %+v err %v", res, err)
	}
	if slices.ContainsFunc(s.ran, func(c string) bool { return strings.Contains(c, "-fuzz ") }) {
		t.Errorf("ran %v", s.ran)
	}
}

// A benchmark more than 10% slower than the base warns; the check still passes.
func TestDeepBenchSlowdownWarns(t *testing.T) {
	head := "pkg: m/a\nBenchmarkEncode-8   1000   1200 ns/op\nBenchmarkParse-8   1000   105 ns/op\n" +
		"BenchmarkNew-8 10 5 ns/op\n"
	base := "pkg: m/a\nBenchmarkEncode-8   1000   1000 ns/op\nBenchmarkParse-8   1000   100 ns/op\n"
	s := basics().on("cd '/base'", reply{0, base}).on("-bench", reply{0, head})
	cfg := DeepConfig{Base: "b0", BaseDir: func(context.Context, string) (string, error) { return "/base", nil }}

	res, err := newTestDeep(cfg).Run(context.Background(), s.run, []DeepKind{DeepBench})
	if err != nil || len(res) != 1 || !res[0].Pass {
		t.Fatalf("res %+v err %v", res, err)
	}
	if !strings.Contains(res[0].Output, "BenchmarkEncode") || strings.Contains(res[0].Output, "BenchmarkParse") ||
		strings.Contains(res[0].Output, "BenchmarkNew") {
		t.Errorf("warning = %q", res[0].Output)
	}
}

// With no base checkout, or no slowdown, the benchmark check passes silently.
func TestDeepBenchNoWarning(t *testing.T) {
	s := basics().on("-bench", reply{0, "pkg: m/a\nBenchmarkEncode-8 1000 1000 ns/op\n"})
	res, err := newTestDeep(DeepConfig{}).Run(context.Background(), s.run, []DeepKind{DeepBench})
	if err != nil || !res[0].Pass || res[0].Output != "" {
		t.Errorf("no base = %+v %v", res, err)
	}

	cfg := DeepConfig{BaseDir: func(context.Context, string) (string, error) { return "/base", nil }}
	s = basics().on("cd '/base'", reply{0, "pkg: m/a\nBenchmarkEncode-8 1000 1000 ns/op\n"}).
		on("-bench", reply{0, "pkg: m/a\nBenchmarkEncode-8 1000 1099 ns/op\n"})
	res, err = newTestDeep(cfg).Run(context.Background(), s.run, []DeepKind{DeepBench})
	if err != nil || !res[0].Pass || res[0].Output != "" {
		t.Errorf("under 10%% = %+v %v", res, err)
	}
}

// Mutation has no tool yet: it is skipped, reported as such, and never fails the group.
func TestDeepMutationSeam(t *testing.T) {
	s := basics()
	res, err := newTestDeep(DeepConfig{}).Run(context.Background(), s.run, []DeepKind{DeepMutation})
	if err != nil || len(res) != 1 || !res[0].Pass || !strings.Contains(res[0].Output, "not built") {
		t.Fatalf("res %+v err %v", res, err)
	}
	if len(s.ran) != 0 {
		t.Errorf("mutation ran %v", s.ran)
	}
}

// Checks run in the order of the plan, one result each, and a language other than Go skips them.
func TestDeepOrderAndLanguage(t *testing.T) {
	s := basics()
	plan := DeepPlan(RiskMedHigh)
	res, err := newTestDeep(DeepConfig{}).Run(context.Background(), s.run, plan)
	if err != nil || len(res) != len(plan) {
		t.Fatalf("res %+v err %v", res, err)
	}
	for i, k := range plan {
		if res[i].Kind != k {
			t.Errorf("result %d = %s, want %s", i, res[i].Kind, k)
		}
	}

	s = basics()
	res, err = newTestDeep(DeepConfig{Language: "python"}).Run(context.Background(), s.run, plan)
	if err != nil || len(s.ran) != 0 {
		t.Fatalf("python ran %v err %v", s.ran, err)
	}
	for _, r := range res {
		if !r.Pass || !strings.Contains(r.Output, "skipped") {
			t.Errorf("python result = %+v", r)
		}
	}
}

// A check that cannot run is an error, not a failed check.
func TestDeepRunError(t *testing.T) {
	boom := errors.New("sandbox refused")
	_, err := newTestDeep(DeepConfig{}).Run(
		context.Background(),
		func(context.Context, string) (int, string, error) { return -1, "", boom },
		[]DeepKind{DeepRace},
	)
	if !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}

// The fuzz time is configurable and defaults to 30 seconds.
func TestDeepFuzzTime(t *testing.T) {
	s := basics().on("-list", reply{0, "FuzzX\n"})
	_, err := newTestDeep(DeepConfig{FuzzTime: 5 * time.Second}).Run(context.Background(), s.run, []DeepKind{DeepFuzz})
	if err != nil || !slices.ContainsFunc(s.ran, func(c string) bool { return strings.Contains(c, "-fuzztime 5s") }) {
		t.Errorf("ran %v err %v", s.ran, err)
	}
}
