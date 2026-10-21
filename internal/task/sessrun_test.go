package task

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// linkRig builds a rig whose only chain link is l, with a passing Quality Assurance and reviewer queued.
func linkRig(t *testing.T, l llm.Link) *pipeRig {
	t.Helper()
	r := newPipeRig(t, rigOpts{chain: []string{string(l)}})
	r.model.on("qa", sayResult(qaPass))
	r.model.on("reviewer", sayResult(reviewerMet))
	return r
}

func TestProgramLinksGetAnMCPConfigAndASessionHandle(t *testing.T) {
	for _, tc := range []struct {
		link     llm.Link
		wantMCP  bool
		wantKind string
	}{
		{llm.ClaudeCLI, true, "session_id"},
		{llm.OpenAISubscription, true, "session_id"},
		{llm.AnthropicAPI, false, "messages_file"},
	} {
		r := linkRig(t, tc.link)
		mcp, kind := false, ""
		r.model.on("builder", func(s llm.Session) (llm.Stream, error) {
			_, err := os.Stat(filepath.Join(s.Dir, "mcp.json"))
			mcp = err == nil
			r.m.mu.Lock()
			kind = r.m.prog.Handles["builder"].Kind
			r.m.mu.Unlock()
			return building(r.wt, "a.go", "package a\n", builderDone)(s)
		})
		r.reachCleanup()
		if mcp != tc.wantMCP || kind != tc.wantKind {
			t.Errorf("%s: mcp.json %v (want %v), handle kind %q (want %q)", tc.link, mcp, tc.wantMCP, kind, tc.wantKind)
		}
	}
}

func TestRejectedResultIsSentBackAsAResumeOnProgramLinks(t *testing.T) {
	for _, tc := range []struct {
		link       llm.Link
		wantResume int
	}{
		{llm.ClaudeCLI, 1},
		{llm.OpenAISubscription, 1},
		{llm.AnthropicAPI, 0},
	} {
		r := linkRig(t, tc.link)
		resumed := 0
		r.model.resume = func(llm.Session, llm.Input) (llm.Stream, error) {
			resumed++
			return building(r.wt, "a.go", "package a\n", builderDone)(llm.Session{})
		}
		r.model.on("builder", sayResult(`{"status":"bogus"}`))
		// A link without a resume gets a second session instead.
		r.model.on("builder", building(r.wt, "a.go", "package a\n", builderDone))
		r.reachCleanup()
		if resumed != tc.wantResume {
			t.Errorf("%s: %d resumes, want %d", tc.link, resumed, tc.wantResume)
		}
	}
}

func TestProgramSessionToolsAreRoutedToTheEngineAndEndWithTheSession(t *testing.T) {
	r := linkRig(t, llm.ClaudeCLI)
	routes := NewToolRoutes()
	r.m.c.Tools, r.m.c.Socket = routes, "/h/state/engine.sock"

	var (
		session, wrote, done string
		advertised           int
	)

	r.model.on("builder", func(s llm.Session) (llm.Stream, error) {
		raw, err := os.ReadFile(filepath.Join(s.Dir, "mcp.json"))
		if err != nil {
			return nil, err
		}

		var doc struct {
			MCPServers map[string]struct {
				Args []string
				Env  map[string]string
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, err
		}

		e := doc.MCPServers["komodo"]
		session = e.Args[len(e.Args)-1]
		if e.Env[EnvSocket] != "/h/state/engine.sock" {
			t.Errorf("mcp.json socket = %q", e.Env[EnvSocket])
		}

		ctx := context.Background()
		defs, _ := routes.Definitions(session)
		advertised = len(defs)
		wrote, _ = routes.Call(ctx, session, "write", json.RawMessage(`{"path":"src/a.go","content":"a\n"}`))
		done, _ = routes.Call(ctx, session, "task_done", json.RawMessage(`{"task":"t1"}`))

		return sayResult(builderDone)(s)
	})
	r.reachCleanup()

	if advertised == 0 || !strings.HasPrefix(wrote, "created src/a.go") || !strings.HasPrefix(done, "exit code 0") {
		t.Errorf("advertised %d tools, write = %q, task_done = %q", advertised, wrote, done)
	}
	if _, ok := routes.Definitions(session); ok {
		t.Error("the route must end with the session")
	}
}

// failingChecks makes a role's Judge reject its first n distinct results as failed checks.
type failingChecks struct {
	Role
	n, seen int
}

// Judge rejects the first n results with a check failure, then defers to the role.
func (f *failingChecks) Judge(ctx context.Context, r llm.Result) (Outcome, error) {
	f.seen++
	if f.seen <= f.n {
		return Outcome{}, &GateError{Text: "tests still fail"}
	}
	return f.Role.Judge(ctx, r)
}

func TestOnlyTheBuilderGateGivesUpAsSchemaFail(t *testing.T) {
	if got := (&judgeMemo{role: &builderRole{}}).CapReason(); got != state.ReasonSchemaFail {
		t.Errorf("builder gate reason = %s, want schema_fail", got)
	}
	if got := (&judgeMemo{role: &qaRole{}}).CapReason(); got != state.ReasonCheckFail {
		t.Errorf("qa gate reason = %s, want check_fail", got)
	}
}

func TestBuilderThatFailsItsChecksIsResumedUntilItPasses(t *testing.T) {
	r := linkRig(t, llm.ClaudeCLI)
	r.m.c.Roles["builder"] = &failingChecks{Role: r.m.c.Roles["builder"], n: 4}

	resumed := 0
	r.model.resume = func(llm.Session, llm.Input) (llm.Stream, error) {
		resumed++
		return sayResult(`{"status":"done","summary":"attempt ` + strings.Repeat("x", resumed) + `"}`)(llm.Session{})
	}
	r.model.on("builder", building(r.wt, "a.go", "package a\n", builderDone))
	r.reachCleanup()
	if resumed != 4 {
		t.Errorf("%d resumes, want 4: failed checks never end the session", resumed)
	}
}
