package agent

import (
	"context"
	"testing"
)

// Go test summary lines carry durations and a rerun may print (cached); a replay still matches them.
func TestQAReplayIgnoresGoTestTimings(t *testing.T) {
	it := Item{ID: "ac-1", Cmd: "go test ./..."}
	excerpt := "ok  \tkomodo/internal/agent\t0.21s\n--- FAIL: TestX (0.00s)\nFAIL\tkomodo/internal/task\t1.5s"
	cases := []struct{ name, out string }{
		{
			"other durations",
			"ok  \tkomodo/internal/agent\t0.35s\n--- FAIL: TestX (0.02s)\nFAIL\tkomodo/internal/task\t12ms\n",
		},
		{
			"cached",
			"ok  \tkomodo/internal/agent\t(cached)\n--- FAIL: TestX (0.00s)\nFAIL\tkomodo/internal/task\t(cached)\n",
		},
	}
	for _, c := range cases {
		run := func(context.Context, string) (int, string, error) { return 0, c.out, nil }
		r := ItemResult{Item: "ac-1", Pass: true, Evidence: []EvidenceRun{{Cmd: "go test ./...", Excerpt: excerpt}}}
		if bad, err := NewQA().Replay(context.Background(), run, it, r, nil); err != nil || len(bad) != 0 {
			t.Errorf("%s: bad %+v err %v", c.name, bad, err)
		}
	}

	other := func(context.Context, string) (int, string, error) {
		return 0, "ok  \tkomodo/internal/other\t0.21s\n", nil
	}
	r := ItemResult{Item: "ac-1", Pass: true, Evidence: []EvidenceRun{
		{Cmd: "go test ./...", Excerpt: "ok  \tkomodo/internal/agent\t0.21s"},
	}}
	if bad, _ := NewQA().Replay(context.Background(), other, it, r, nil); len(bad) != 1 {
		t.Errorf("another package matched: %+v", bad)
	}
}

// NormalizeOutput makes two runs of the same test comparable.
func TestNormalizeOutput(t *testing.T) {
	a := NormalizeOutput("--- FAIL: TestX (0.00s)\nok  \tp\t0.21s")
	b := NormalizeOutput("--- FAIL: TestX (1.25s)\nok  \tp\t(cached)")
	if a != b {
		t.Errorf("%q != %q", a, b)
	}
}
