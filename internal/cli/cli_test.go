package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
)

func TestOutcomeValuesAreExitCodes(t *testing.T) {
	want := map[Outcome]int{OutcomeOK: 0, OutcomeRefused: 1, OutcomeUsage: 2, OutcomeHeld: 3, OutcomeInternal: 4}
	for o, code := range want {
		if int(o) != code {
			t.Errorf("outcome %d != exit %d", o, code)
		}
	}
}

func TestClosestMatch(t *testing.T) {
	h := newHarness(t)
	code, e := h.envelope("rnu")
	if code != 2 || e.Error == nil || e.Error.Code != "usage" || !strings.Contains(e.Error.Reason+e.Error.Fix, "run") {
		t.Fatalf("rnu: exit %d, %+v", code, e.Error)
	}
	if !strings.Contains(e.Error.Fix, "komodo run") {
		t.Fatalf("fix does not name run: %q", e.Error.Fix)
	}
	code, e = h.envelope("qqqqqqqq")
	if code != 2 || !strings.Contains(e.Error.Fix, "komodo --help") {
		t.Fatalf("far name: exit %d, fix %q", code, e.Error.Fix)
	}
}

func TestUnknownFlagNamesClosest(t *testing.T) {
	h := newHarness(t)
	code, e := h.envelope("run", "tg-1.2", "--schedul", "x")
	if code != 2 || !strings.Contains(e.Error.Fix, "--schedule") {
		t.Fatalf("exit %d, %+v", code, e.Error)
	}
}

func TestVersionTouchesNothing(t *testing.T) {
	h := newHarness(t)
	code, e := h.envelope("--version")
	if code != 0 || !e.OK {
		t.Fatalf("exit %d, %+v", code, e)
	}
	d, _ := e.Data.(map[string]any)
	if v, _ := d["version"].(string); !strings.Contains(v, "1.0.0") {
		t.Fatalf("data = %#v", e.Data)
	}
	if _, err := os.Stat(filepath.Join(h.home, "state")); err == nil {
		t.Fatal("--version touched the home folder")
	}
}

func TestEnvelopeShapeAndEmptyData(t *testing.T) {
	h := newHarness(t)
	_, out, _ := h.run("--json", "rnu")
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"v", "ok", "verdict", "report", "data", "error"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("envelope lacks %q: %s", k, out)
		}
	}
	if string(raw["data"]) != "{}" || string(raw["v"]) != "1" {
		t.Errorf("data = %s, v = %s", raw["data"], raw["v"])
	}
}

func TestNoTerminalRefusesTerminalOnly(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{{"install"}, {"update"}, {"config", "set", "limits.group_retries=4"}} {
		code, e := h.envelope(args...)
		if code != 1 || e.Error == nil || e.Error.Code != "no_terminal" {
			t.Errorf("%v: exit %d, %+v", args, code, e.Error)
		}
	}
}

// The orchestrator has no terminal, so it cannot give a spent group an override; approve and deny stay open to it.
func TestOverrideNeedsATerminal(t *testing.T) {
	h := newHarness(t)
	code, e := h.envelope("run", "tg-1.2", "--override")
	if code != 1 || e.Error == nil || e.Error.Code != "no_terminal" {
		t.Fatalf("exit %d, %+v", code, e.Error)
	}
	if _, e := h.envelope("run", "tg-1.2", "--override", "--approve"); e.Error == nil || e.Error.Code != "usage" {
		t.Fatalf("--override with --approve: %+v", e.Error)
	}
}

func TestPinnedRepository(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "99.0.0")
	code, e := h.envelope("-C", repo, "run", "tg-1.2")
	if code != 1 || e.Error.Code != "pinned" || !strings.Contains(e.Error.Fix, "komodo update") {
		t.Fatalf("run: exit %d, %+v", code, e.Error)
	}
	if code, out, _ := h.run("-C", repo, "--help"); code != 0 || !strings.Contains(out, "Commands:") {
		t.Fatalf("--help in a pinned repository: exit %d\n%s", code, out)
	}
	for _, args := range [][]string{{"--version"}, {"doctor"}, {"update"}, {"install"}} {
		code, e := h.envelope(append([]string{"-C", repo}, args...)...)
		if e.Error != nil && e.Error.Code == "pinned" {
			t.Errorf("%v was refused as pinned (exit %d)", args, code)
		}
	}
}

func TestPinAtOrBelowBinaryRuns(t *testing.T) {
	h := newHarness(t)
	h.engine = &scriptEngine{fn: func(engine.Request) []engine.Reply { return []engine.Reply{okReply(map[string]any{})} }}
	repo := repoDir(t, "1.0.0-beta.5")
	if code, e := h.envelope("-C", repo, "clean"); code != 0 {
		t.Fatalf("exit %d, %+v", code, e.Error)
	}
}

func TestTamperPolicy(t *testing.T) {
	h := newHarness(t)
	h.engine = &scriptEngine{fn: func(engine.Request) []engine.Reply {
		return []engine.Reply{okReply(map[string]any{"run_id": "0192e4c5-6f00-7a21-8c3b-2d9e0f1a4b57"})}
	}}
	if err := os.WriteFile(filepath.Join(h.home, "settings.json"), []byte(`{"limits.group_retries": 2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := repoDir(t, "")
	code, out, errOut := h.run("--json", "-C", repo, "stop", "tg-1.2")
	if code != 0 || !strings.Contains(errOut, "komodo config set --accept") || strings.Count(out, `"v"`) != 1 {
		t.Fatalf("stop: exit %d\nstdout=%s\nstderr=%s", code, out, errOut)
	}
	code, e := h.envelope("-C", repo, "run", "tg-1.2")
	if code != 1 || e.Error.Code != "config_tampered" || e.Error.Fix != "komodo config set --accept" {
		t.Fatalf("run: exit %d, %+v", code, e.Error)
	}
}

func TestInputMergeFlagWins(t *testing.T) {
	h := newHarness(t)
	se := &scriptEngine{fn: func(engine.Request) []engine.Reply { return []engine.Reply{okReply(map[string]any{})} }}
	h.engine = se
	repo := repoDir(t, "")
	in := filepath.Join(t.TempDir(), "in.json")
	if err := os.WriteFile(in, []byte(`{"timeout":"1m","wait":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	h.envelope("-C", repo, "status", "--input", in, "--timeout", "2m")
	if len(se.seen) != 1 || se.seen[0].Flags.TimeoutS != 120 || !se.seen[0].Flags.Wait {
		t.Fatalf("request = %+v", se.seen)
	}
	if err := os.WriteFile(in, []byte(`{"nope":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	code, e := h.envelope("-C", repo, "status", "--input", in)
	if code != 2 || e.Error.Code != "usage" {
		t.Fatalf("unknown key: exit %d, %+v", code, e.Error)
	}
	code, e = h.envelope("status", "--input", filepath.Join(t.TempDir(), "missing.json"))
	if code != 2 || !strings.Contains(e.Error.Reason, "missing.json") {
		t.Fatalf("missing file: exit %d, %+v", code, e.Error)
	}
}

func TestHelpMatchesTable(t *testing.T) {
	h := newHarness(t)
	code, out, _ := h.run("--help", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var docs []struct {
		Name    string `json:"name"`
		Summary string `json:"summary"`
		Tier    string `json:"tier"`
		Flags   []struct {
			Name string `json:"name"`
		} `json:"flags"`
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &docs); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(docs) != len(commands()) {
		t.Fatalf("help lists %d commands, table holds %d", len(docs), len(commands()))
	}
	for i, c := range commands() {
		d := docs[i]
		if d.Name != c.Name || d.Summary != c.Summary || len(d.Flags) < len(c.Flags) || d.Result["$schema"] == nil {
			t.Errorf("%s: help = %+v", c.Name, d)
		}
	}
	names := map[string]bool{}
	for _, d := range docs {
		names[strings.Fields(d.Name)[0]] = true
	}
	if len(names) != 13 {
		t.Errorf("help names %d commands, want 13: %v", len(names), names)
	}
}

func TestCommandTextHelp(t *testing.T) {
	h := newHarness(t)
	code, out, _ := h.run("run", "--help")
	if code != 0 || !strings.Contains(out, "--schedule") || !strings.Contains(out, "--approve") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

func TestPanicExitsFour(t *testing.T) {
	h := newHarness(t)
	h.engine = &scriptEngine{fn: func(engine.Request) []engine.Reply { panic("boom") }}
	repo := repoDir(t, "")
	code, e := h.envelope("-C", repo, "clean")
	if code != 4 || e.Error == nil || e.Error.Code != "internal" {
		t.Fatalf("exit %d, %+v", code, e)
	}
}
