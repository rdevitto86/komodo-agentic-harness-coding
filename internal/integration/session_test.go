//go:build !windows

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	komodo "github.com/rdevitto86/komodo-agentic-harness-coding"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cache"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/config"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/contextmgr"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// fixtureRepo is the small Go module the Builder works on.
const fixtureRepo = "testdata/repo"

// rig holds one wired machine: real packages from the model client to the event bus.
type rig struct {
	home   string
	cfg    *config.Manager
	client *llm.Client
	probes []llm.ProbeResult
	api    *replayAPI
	claude *fakeClaude
	cache  *cache.Cache
	cm     *contextmgr.Manager
	bus    bus.Bus
	sub    *bus.Subscription
	tree   dirTree
	role   config.Agent
	events string
}

// newRig wires config, the model client, the cache, the Context Manager and the bus over recorded providers.
func newRig(t *testing.T, claudeOut []string, apiReplies ...func(w http.ResponseWriter)) *rig {
	t.Helper()
	r := &rig{home: t.TempDir(), tree: newDirTree(t, fixtureRepo)}
	t.Setenv("KOMODO_HOME", r.home)
	r.api = newReplayAPI(t, apiReplies...)
	r.claude = installClaude(t, claudeOut...)
	cat, err := llm.LoadCatalog()
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	cl := llm.NewClaude(llm.ClaudeOptions{
		Fallbacks: []string{filepath.Join(r.home, "none", "claude")},
		Env:       r.claude.env(),
		Catalog:   cat,
	})
	an, err := llm.NewAnthropic(llm.AnthropicOptions{BaseURL: r.api.srv.URL, Key: apiKey, Catalog: cat})
	if err != nil {
		t.Fatalf("anthropic: %v", err)
	}
	r.client = llm.New(map[llm.Link]llm.Provider{llm.ClaudeCLI: cl, llm.AnthropicAPI: an})
	r.probes = r.client.ProbeAll(context.Background())
	for _, p := range r.probes {
		if !p.OK {
			t.Fatalf("probe %s failed: %s", p.Link, p.Reason)
		}
	}
	if r.cfg, err = config.New(r.home, knownFor(), llm.NewModelCheck(cat, r.probes...)); err != nil {
		t.Fatalf("config: %v", err)
	}
	var ok bool
	if r.role, ok = r.cfg.GetAgent("builder"); !ok {
		t.Fatal("config has no builder role")
	}
	if r.cache, err = cache.New(filepath.Join(r.home, "cache"), version, cache.Limits{}); err != nil {
		t.Fatalf("cache: %v", err)
	}
	r.events = filepath.Join(r.home, "events")
	r.bus = bus.New(bus.Options{EventsDir: r.events})
	t.Cleanup(func() { _ = r.bus.Close() })
	if r.sub, err = r.bus.Subscribe("proof", bus.TypeRunStatus); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	tools := map[string][]string{}
	for _, name := range []string{"planner", "builder", "qa", "reviewer"} {
		a, _ := r.cfg.GetAgent(name)
		tools[name] = a.Tools
	}
	r.cm, err = contextmgr.New(contextmgr.Deps{
		Bus: r.bus, Cache: r.cache.Handle(), Home: r.home, Tools: tools, Items: itemStates(t, "builder", r.role.Skills),
	})
	if err != nil {
		t.Fatalf("context manager: %v", err)
	}
	err = r.bus.Publish(bus.Event{Type: bus.TypeRunStart, Run: run, Data: &bus.RunStart{
		Target: "tg-1.2", Groups: []string{"tg-1.2"}, Source: "cli", Repo: repo,
		Root: filepath.Join(r.home, "src"), CommonDir: filepath.Join(r.home, "src", ".git"),
	}})
	if err != nil {
		t.Fatalf("publish run-start: %v", err)
	}
	return r
}

// session builds the Builder's brief with the Context Manager and the llm.Session for one link.
func (r *rig) session(t *testing.T, link llm.Link, id string) (llm.Session, contextmgr.Context) {
	t.Helper()
	ctx := context.Background()
	prof, err := r.client.Profile(ctx, link, llm.Weight(r.role.Weight))
	if err != nil {
		t.Fatalf("profile %s: %v", link, err)
	}
	src, err := r.tree.ReadFile("calc/calc.go")
	if err != nil {
		t.Fatal(err)
	}
	chain := []contextmgr.Candidate{{Link: string(link), Ctx: prof.Ctx}}
	got, err := r.cm.GetContext(ctx, contextmgr.Request{
		Agent: "builder", Run: run, Group: group, Tree: r.tree, Files: []string{"calc/calc.go"}, Chain: chain,
		Slots: contextmgr.Values{
			contextmgr.Group:        "tg-1.2: calculator",
			contextmgr.Tasks:        "1. Add Mul(a, b int) int to calc/calc.go and test it in calc/calc_test.go.",
			contextmgr.Files:        "calc/calc.go\ncalc/calc_test.go",
			contextmgr.FileContents: "calc/calc.go\n```go\n" + string(src) + "```",
		},
	})
	if err != nil {
		t.Fatalf("get context: %v", err)
	}
	if _, err := got.Brief.Place(chain, nil); err != nil {
		t.Fatalf("brief does not fit %s: %v", link, err)
	}
	dir := t.TempDir()
	sys, err := komodo.Embedded.ReadFile("embed/output/standard.md")
	if err != nil {
		t.Fatalf("read output format: %v", err)
	}
	for name, body := range map[string]string{"brief.md": got.Brief.Text(), "system.md": string(sys), "mcp.json": "{}"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return llm.Session{
		ID: id, Dir: dir, Agent: "builder", Link: link, Model: r.model(t, link), Profile: prof, Schema: r.role.Result,
		Tools: &recordHost{names: r.role.Tools, tree: r.tree}, Env: r.claude.env(), Cache: r.cache.Handle(),
		Parts: got.Brief.Parts, Gate: roleGate{},
	}, got
}

// model resolves the builder's model on a link: the config override, else the probe's pick for the weight.
func (r *rig) model(t *testing.T, link llm.Link) string {
	t.Helper()
	it, err := r.cfg.GetConfigItem("agents.builder.model")
	if err != nil {
		t.Fatalf("read builder model: %v", err)
	}
	v, _ := it.Value.(string)
	w, isWeight := strings.CutPrefix(v, "weight:")
	if !isWeight {
		return v
	}
	for _, p := range r.probes {
		if p.Link == link && p.Models[llm.Weight(w)] != "" {
			return p.Models[llm.Weight(w)]
		}
	}
	t.Fatalf("probe of %s found no model for weight %s", link, w)
	return ""
}

// drive runs one session to its end and publishes its start and end to the bus, as the Task Manager does.
func (r *rig) drive(t *testing.T, s llm.Session) (*llm.Result, []llm.StreamEvent, error) {
	t.Helper()
	facts := bus.GroupFacts{Stage: string(state.StageBuild), Session: bus.ID(s.ID)}
	publish := func(sub bus.Subtype, d bus.Payload) {
		t.Helper()
		if err := r.bus.Publish(bus.Event{
			Type:    bus.TypeRunStatus,
			Subtype: sub,
			Run:     run,
			Group:   group,
			Data:    d,
		}); err != nil {
			t.Fatalf("publish %s: %v", sub, err)
		}
	}
	publish("session.start", &bus.SessionStart{
		GroupFacts: facts,
		Agent:      s.Agent,
		Task:       "1",
		Link:       string(s.Link),
		Model:      s.Model,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	begin := time.Now()
	end := &bus.SessionEnd{GroupFacts: facts, Agent: s.Agent, Task: "1", Link: string(s.Link)}
	end.Reason = string(state.ReasonDone)
	var evs []llm.StreamEvent
	var res *llm.Result
	st, err := r.client.Start(ctx, s)
	for err == nil {
		var ev llm.StreamEvent
		if ev, err = st.Next(ctx); err == nil {
			evs = append(evs, ev)
			if ev.Kind == llm.KindResult {
				res = ev.Result
			}
		}
	}
	if st != nil {
		_ = st.Close()
	}
	if errors.Is(err, io.EOF) {
		err = nil
	}
	if res != nil {
		u := res.Usage
		end.Handle, end.Tokens = res.Handle.Value, int64(u.TokensIn+u.TokensOut)
		end.Usage = &bus.SessionUsage{
			TokensIn:   u.TokensIn,
			TokensOut:  u.TokensOut,
			CacheRead:  u.CacheRead,
			CacheWrite: u.CacheWrite,
			USD:        u.USD,
			USDEquiv:   u.USDEquiv,
		}
	}
	var ee *llm.EndError
	if errors.As(err, &ee) {
		end.Reason = string(ee.Reason)
	}
	end.WallS = int64(time.Since(begin).Seconds())
	publish("session.end", end)
	return res, evs, err
}

// replay closes the bus and returns the run's events as the event store holds them.
func (r *rig) replay(t *testing.T) bus.ReplayLog {
	t.Helper()
	if err := r.bus.Close(); err != nil {
		t.Fatalf("close bus: %v", err)
	}
	log, err := bus.NewReader(r.events).Replay(context.Background(), run, "")
	if err != nil {
		t.Fatalf("replay run: %v", err)
	}
	return log
}

// checkAPILink asserts what the paid link sent: the brief, the role's tools, the markers, and the tool call that ran.
func (r *rig) checkAPILink(t *testing.T, s llm.Session) {
	t.Helper()
	bodies, headers := r.api.requests()
	if len(bodies) != 3 {
		t.Fatalf("messages requests = %d, want 3", len(bodies))
	}
	if headers[0].Get("X-Api-Key") != apiKey || headers[0].Get("Anthropic-Version") == "" {
		t.Errorf("request headers = %v", headers[0])
	}
	first := bodies[0]
	for _, want := range []string{`"model":"claude-sonnet-5-5"`, `"stream":true`, "func Add", "Use tabs", `"ttl":"1h"`} {
		if !strings.Contains(first, want) {
			t.Errorf("first request lacks %s", want)
		}
	}
	var req struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(first), &req); err != nil {
		t.Fatalf("decode first request: %v", err)
	}
	var names []string
	for _, tl := range req.Tools {
		names = append(names, tl.Name)
	}
	slices.Sort(names)
	if want := slices.Sorted(slices.Values(r.role.Tools)); !slices.Equal(names, want) {
		t.Errorf("advertised tools = %v, want %v", names, want)
	}
	if !strings.Contains(bodies[1], "func Add") || !strings.Contains(bodies[1], "toolu_01ReadCalc") {
		t.Errorf("second request does not carry the tool result")
	}
	host := s.Tools.(*recordHost)
	if got := host.calledWith(); !slices.Equal(got, []string{`read {"path":"calc/calc.go"}`}) {
		t.Errorf("tool host calls = %v", got)
	}
	raw, hit := r.cache.Handle().Get(cache.NSPrompt, r.cache.Key("plan", "anthropic-api", "builder", s.Model))
	var plan cache.MarkerPlan
	if !hit || json.Unmarshal(raw, &plan) != nil || len(plan.Layers) != 5 ||
		!slices.Equal(plan.Fixed, []string{"repo", "brief"}) {
		t.Errorf("stored marker plan = %s hit %v", raw, hit)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "messages.jsonl")); err != nil {
		t.Errorf("session folder: %v", err)
	}
}

// checkClaudeLink asserts what the subscription link launched and sent to the claude program.
func (r *rig) checkClaudeLink(t *testing.T, s llm.Session, res *llm.Result) {
	t.Helper()
	args := strings.Split(strings.TrimSpace(r.claude.read("args.txt")), "\n")
	at := slices.Index(args, "--json-schema")
	if at < 0 || args[at+1] != string(r.role.Result) || !slices.Contains(args, "--session-id") ||
		!slices.Contains(args, s.Model) {
		t.Errorf("claude launch line = %v", args)
	}
	if env := r.claude.read("env.txt"); strings.Contains(env, "sk-planted") {
		t.Error("the planted API key reached the claude program")
	}
	stdin := r.claude.read("stdin.txt")
	for _, want := range []string{"func Add", "Use tabs", "Add Mul(a, b int) int"} {
		if !strings.Contains(stdin, want) {
			t.Errorf("claude input lacks %q", want)
		}
	}
	if calls := s.Tools.(*recordHost).calledWith(); len(calls) != 0 {
		t.Errorf("tool host ran %v; the claude program runs tools through its own server", calls)
	}
	if res.Handle.Kind != "session_id" || res.Handle.Value != s.ID {
		t.Errorf("handle = %+v", res.Handle)
	}
}

// TestOneAgentSessionRunsEndToEnd runs the Builder through every link of its chain, from config to a validated result.
func TestOneAgentSessionRunsEndToEnd(t *testing.T) {
	r := newRig(t, claudeLines(finalJSON), toolTurn,
		textTurn("calc/calc.go is read. Mul and its test are added.", 3100, 40), textTurn(finalJSON, 3300, 30))
	if got := r.role.Chain; !slices.Equal(got, []string{"claude-cli", "anthropic-api"}) {
		t.Fatalf("builder chain = %v", got)
	}
	for _, link := range r.role.Chain {
		t.Run(link, func(t *testing.T) {
			s, got := r.session(t, llm.Link(link), sessionID)
			if s.Model != "claude-sonnet-5-5" {
				t.Fatalf("model = %q", s.Model)
			}
			text := got.Brief.Text()
			for _, want := range []string{
				"# Tools",
				"- task_done",
				"You are the builder",
				"func Add",
				"Use tabs",
				"Add Mul(a, b int) int",
			} {
				if !strings.Contains(text, want) {
					t.Errorf("brief lacks %q", want)
				}
			}
			if names := skillNames(got); !slices.Equal(names, []string{"standards-comments", "standards-go"}) {
				t.Errorf("brief skills = %v", names)
			}
			res, evs, err := r.drive(t, s)
			if err != nil || res == nil {
				t.Fatalf("session: result %v err %v", res, err)
			}
			br, err := agent.ParseBuilderResult(res.Output)
			if err != nil || br.Status != agent.BuilderDone || br.Summary == "" {
				t.Fatalf("parsed result = %+v err %v", br, err)
			}
			if len(evs) < 2 || evs[len(evs)-1].Kind != llm.KindResult {
				t.Errorf("stream events = %d, last %v", len(evs), evs[len(evs)-1].Kind)
			}
			if link == "anthropic-api" {
				r.checkAPILink(t, s)
			} else {
				r.checkClaudeLink(t, s, res)
			}
		})
	}

	log := r.replay(t)
	ends := sessionEnds(log)
	if len(ends) != 2 || ends[0].Link != "claude-cli" || ends[1].Link != "anthropic-api" {
		t.Fatalf("session.end events = %+v", ends)
	}
	for _, e := range ends {
		if e.Reason != string(state.ReasonDone) || e.Tokens <= 0 || e.Usage == nil || e.Handle == "" || e.Stage != "build" {
			t.Errorf("session.end of %s = %+v usage %+v", e.Link, e, e.Usage)
		}
	}
	if ends[0].Usage.USD != 0 || ends[0].Usage.USDEquiv <= 0 || ends[1].Usage.USD <= 0 || ends[1].Usage.USDEquiv != 0 {
		t.Errorf("cost classes: subscription %+v, paid %+v", ends[0].Usage, ends[1].Usage)
	}
	var kinds []string
	for _, e := range log.Events {
		kinds = append(kinds, string(e.Type)+"/"+string(e.Subtype))
	}
	want := []string{
		"run-start/",
		"run-status/session.start",
		"run-status/session.end",
		"run-status/session.start",
		"run-status/session.end",
	}
	if !slices.Equal(kinds, want) || len(log.Gaps) != 0 {
		t.Errorf("event store holds %v gaps %v, want %v", kinds, log.Gaps, want)
	}
	live := 0
	for range r.sub.Events {
		live++
	}
	if live != 4 {
		t.Errorf("subscriber saw %d run-status events, want 4", live)
	}
}

// TestRolesInConfigMatchCompiledRoles checks the embedded role files against the compiled roles in the agent package.
func TestRolesInConfigMatchCompiledRoles(t *testing.T) {
	cfg, err := config.New(t.TempDir(), knownFor(), func(string) bool { return true })
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	for _, role := range agent.Roles() {
		a, ok := cfg.GetAgent(role.Name)
		if !ok {
			t.Fatalf("config has no %s role", role.Name)
		}
		if !slices.Equal(a.Tools, role.Tools) || !slices.Equal(a.RunClasses, role.RunClasses) ||
			!slices.Equal(a.Guard, role.Guard) ||
			!slices.Equal(
				a.Slots.Required,
				role.Slots.Required,
			) || !slices.Equal(a.Slots.Optional, role.Slots.Optional) || a.PoolSlot != role.PoolSlot {
			t.Errorf("%s: config %+v differs from compiled %+v", role.Name, a, role)
		}
		var fromConfig, compiled any
		if err := json.Unmarshal(a.Result, &fromConfig); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(role.Result, &compiled); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(fromConfig, compiled) {
			t.Errorf("%s: result schema in config %s differs from compiled %s", role.Name, a.Result, role.Result)
		}
	}
}

// TestConfigRefusesAModelTheProbeDoesNotKnow checks the model check built from the catalog and the real probes.
func TestConfigRefusesAModelTheProbeDoesNotKnow(t *testing.T) {
	r := newRig(t, claudeLines(finalJSON))
	set := func(m *config.Manager, model string) error {
		_, err := m.SetConfigItem(config.Change{Key: "agents.builder.model", New: model})
		return err
	}
	for _, bad := range []string{"gpt-9-turbo", "claude-sonnet-9-9", "Qwen3.6-27B"} {
		var le config.LimitError
		if err := set(r.cfg, bad); !errors.As(err, &le) || le.Value != bad {
			t.Errorf("set %q: err %v, want a LimitError", bad, err)
		}
	}
	if it, _ := r.cfg.GetConfigItem("agents.builder.model"); it.Value != "weight:standard" {
		t.Errorf("a refused model changed the config: %v", it.Value)
	}
	if err := set(r.cfg, "claude-sonnet-5"); err != nil {
		t.Errorf("a catalog model the probes can use was refused: %v", err)
	}
	if it, _ := r.cfg.GetConfigItem("agents.builder.model"); it.Value != "claude-sonnet-5" ||
		it.Source != config.LayerMachine {
		t.Errorf("accepted model = %+v", it)
	}
	if got := r.model(t, llm.AnthropicAPI); got != "claude-sonnet-5" {
		t.Errorf("session model = %q, want the override", got)
	}

	cat, _ := llm.LoadCatalog()
	down, err := config.New(
		t.TempDir(),
		knownFor(),
		llm.NewModelCheck(cat, llm.ProbeResult{Link: llm.AnthropicAPI, Reason: "no API key is set"}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := set(down, "claude-sonnet-5-5"); err == nil {
		t.Error("a model was accepted although every probe failed")
	}
}

// TestRoleCheckSendsABadResultBack runs the paid link with a first result that passes the schema but not the role's check.
func TestRoleCheckSendsABadResultBack(t *testing.T) {
	bad := `{"status":"done","summary":"Added Mul.","paths":["calc/other.go"]}`
	r := newRig(t, claudeLines(finalJSON), toolTurn,
		textTurn(
			"Mul is added.",
			3100,
			20,
		), textTurn(bad, 3300, 30), textTurn("Fixing the result.", 3400, 10), textTurn(finalJSON, 3500, 30))
	s, _ := r.session(t, llm.AnthropicAPI, sessionID)
	res, _, err := r.drive(t, s)
	if err != nil || res == nil {
		t.Fatalf("session: %v", err)
	}
	if br, err := agent.ParseBuilderResult(res.Output); err != nil || br.Status != agent.BuilderDone {
		t.Fatalf("final result = %s err %v", res.Output, err)
	}
	bodies, _ := r.api.requests()
	if len(bodies) != 5 || !strings.Contains(bodies[3], "Your result was refused") {
		t.Fatalf("requests = %d; the refusal did not reach the model", len(bodies))
	}
}

// TestResultBreakingTheRoleSchemaEndsTheSession runs the subscription link with a result that lacks the required summary.
func TestResultBreakingTheRoleSchemaEndsTheSession(t *testing.T) {
	r := newRig(t, claudeLines(`{"status":"done"}`))
	s, _ := r.session(t, llm.ClaudeCLI, sessionID)
	res, _, err := r.drive(t, s)
	var ee *llm.EndError
	if !errors.As(err, &ee) || ee.Reason != state.ReasonSchemaFail || res != nil {
		t.Fatalf("result %v err %v, want a schema_fail end", res, err)
	}
	ends := sessionEnds(r.replay(t))
	if len(ends) != 1 || ends[0].Reason != string(state.ReasonSchemaFail) {
		t.Errorf("session.end events = %+v", ends)
	}
}

// claudeLines is the recorded stream-json of one subscription session: a read, a reply, and the structured result.
func claudeLines(structured string) []string {
	return []string{
		`{"type":"system","subtype":"init","apiKeySource":"none","session_id":"` + sessionID +
			`","model":"claude-sonnet-5-5"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_01ReadCalc",` +
			`"name":"mcp__komodo__read","input":{"path":"calc/calc.go"}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01ReadCalc",` +
			`"content":"package calc"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Mul added and tested."}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"structured_output":` + structured +
			`,"total_cost_usd":0.0412,` +
			`"usage":{"input_tokens":2100,"output_tokens":310,"cache_read_input_tokens":1800,` +
			`"cache_creation_input_tokens":600}}`,
	}
}

// sessionEnds returns the session.end data in a replay, in order.
func sessionEnds(log bus.ReplayLog) []*bus.SessionEnd {
	var out []*bus.SessionEnd
	for _, e := range log.Events {
		if d, ok := e.Data.(*bus.SessionEnd); ok {
			out = append(out, d)
		}
	}
	return out
}

// skillNames lists the standards a brief carries.
func skillNames(c contextmgr.Context) []string {
	var out []string
	for _, s := range c.Skills {
		out = append(out, string(s.Name))
	}
	return out
}
