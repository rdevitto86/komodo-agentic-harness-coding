package cli

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
)

var update = flag.Bool("update", false, "rewrite the golden envelopes")

// goldenReplies answers each verb with a fixed reply so the envelopes are stable.
func goldenReplies(_ *fakeServer, req engine.Request, send func(engine.Reply)) {
	switch req.Verb {
	case engine.VerbPlan:
		send(engine.Reply{Status: engine.StatusEvent, Event: &bus.Event{
			Type: bus.TypeRunStart, Run: "0192e5a0-3b44-7d10-8c62-9a1f4e7b0d25",
			Data: &bus.RunStart{Target: "ep-2", Groups: []string{"tg-2.1", "tg-2.2"}, Source: "intake"},
		}})
		send(okReply(map[string]any{}))
	case engine.VerbReplay:
		send(engine.Reply{Status: engine.StatusEvent, Event: &bus.Event{Type: bus.TypeRunComplete, Subtype: "stopped", Run: sampleRun, Data: &bus.RunStopped{}}})
		send(okReply(map[string]any{"gaps": 0}))
	case engine.VerbStatus:
		send(okReply(statusFixture()))
	case engine.VerbClean:
		send(okReply(CleanData{Removed: []CleanItem{{Kind: "worktree", Name: "tg-1.2"}}}))
	case engine.VerbAbandon:
		send(okReply(map[string]any{"run_id": sampleRun}))
	default:
		answerRunID(nil, req, send)
	}
}

func TestGoldenEnvelopes(t *testing.T) {
	cases := []struct {
		name     string
		terminal bool
		args     []string
	}{
		{"install", true, []string{"install", "--repo-only", "--hosts", "claude"}},
		{"config-get", false, []string{"config", "get", "limits.returns"}},
		{"config-set", true, []string{"config", "set", "limits.returns=2"}},
		{"plan", false, []string{"plan", "Add lease revocation"}},
		{"run", false, []string{"run", "tg-1.2"}},
		{"stop", false, []string{"stop", "tg-1.2"}},
		{"replay", false, []string{"replay", "tg-1.2"}},
		{"status", false, []string{"status"}},
		{"abandon", false, []string{"abandon", "tg-1.2"}},
		{"ui", false, []string{"ui"}},
		{"doctor", false, []string{"doctor"}},
		{"update", true, []string{"update", "--check"}},
		{"clean", false, []string{"clean"}},
		{"refusal-no-terminal", false, []string{"config", "set", "limits.returns=4"}},
		{"usage-unknown-command", false, []string{"rnu"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.terminal = c.terminal
			h.fake(goldenReplies)
			h.host.Release = newRelease(t, "1.0.1", "golden").config()
			repo := repoDir(t, "")
			backlog(t, repo, map[string]string{"epic-1/tg-1.2": "todo"})
			code, out, errOut := h.run(append([]string{"--json", "-C", repo}, c.args...)...)
			if errOut != "" {
				t.Fatalf("stderr = %q", errOut)
			}
			got := uuidPattern.ReplaceAllString(out, "<uuid>")
			got = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(got, repo, "<repo>"), h.home, "<home>"), h.userHome, "<user>")
			got = "exit " + string(rune('0'+code)) + "\n" + got
			path := filepath.Join("testdata", "golden", c.name+".txt")
			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("no golden file (run go test -update): %v", err)
			}
			if string(want) != got {
				t.Fatalf("envelope differs from %s\n--- want\n%s\n--- got\n%s", path, want, got)
			}
			var env Envelope
			if err := json.Unmarshal([]byte(out), &env); err != nil || env.V != 1 {
				t.Fatalf("not an envelope: %v", err)
			}
		})
	}
}
