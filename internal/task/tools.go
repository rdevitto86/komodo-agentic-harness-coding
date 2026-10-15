package task

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/sandbox"
)

// Reply is the Task Manager's name for what the model receives for one tool call.
type Reply = llm.ToolReply

// Bound satisfies the host the model client's tool loop calls, and describes its tools to it.
var (
	_ llm.ToolHost    = (*Bound)(nil)
	_ llm.ToolDefiner = (*Bound)(nil)
)

// Confiner starts commands inside a sandbox instance; *sandbox.Manager is one.
type Confiner interface {
	// SetMasked replaces the unreadable file list of an instance.
	SetMasked(id string, masked []string) error
	// Command wraps one command so that it starts inside the instance.
	Command(ctx context.Context, id string, c sandbox.Command) (*exec.Cmd, error)
}

var _ Confiner = (*sandbox.Manager)(nil)

// GitReader is the host git as the diff and git_read tools use it.
type GitReader interface {
	// Diff returns the unified diff of the group's change against its base commit, for the paths given or for all.
	Diff(ctx context.Context, paths []string) (string, error)
	// ReadOnly runs log, show, blame or status and returns its output.
	ReadOnly(ctx context.Context, command string, args []string) (string, error)
}

// Submitted is one finding an agent filed through submit_finding.
type Submitted struct {
	agent.Finding
	Note string
}

// Env is everything the tools of one session reach.
type Env struct {
	Worktree   string                                                      // absolute worktree path
	Dir        string                                                      // the session folder; out/ and tools.jsonl live in it, and none is written when empty
	Confiner   Confiner                                                    // wraps run commands
	Instance   string                                                      // the sandbox instance id for this agent's role
	Commands   map[string][]string                                         // command class to the argv the run tool starts
	Format     []string                                                    // the format command's argv; the edited file path is appended
	RunTimeout time.Duration                                               // zero means 10 minutes
	Git        GitReader                                                   // host git for diff and git_read
	Skill      func(ref string) (string, error)                            // the text of a skill or one of its sections
	Done       func(ctx context.Context, task string) (int, string, error) // reruns a task's done_when command and returns its exit code and output
	Lint       func(draft string) []string                                 // lint errors of a planned task list; empty means clean
	OnFinding  func(Submitted)                                             // receives each validated finding
	OnFact     func(Fact)                                                  // receives tool.denied, tool.outside and finding.submit
	Now        func() time.Time                                            // clock; time.Now when nil
}

// now returns the current time.
func (e *Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// toolCall is what one tool function receives besides its arguments.
type toolCall struct {
	env   *Env
	guard *Guard
	bound *Bound
	id    string
}

// tool is one registered tool: its definition and its function.
type tool struct {
	def llm.ToolDef
	run func(ctx context.Context, tc *toolCall, args json.RawMessage) Reply
}

// outsideTool is one tool of an outside server; it returns the reply text and the outcome (ok, error or timeout).
type outsideTool struct {
	def llm.ToolDef
	run func(ctx context.Context, args json.RawMessage) (string, string)
}

// Registry holds every tool Go knows, built-in and outside, for one session's environment.
type Registry struct {
	env     *Env
	tools   map[string]tool
	outside map[string]outsideTool
}

// NewRegistry registers the built-in tools over env.
func NewRegistry(env *Env) *Registry {
	r := &Registry{env: env, tools: map[string]tool{}, outside: map[string]outsideTool{}}
	for _, t := range builtinTools() {
		r.tools[t.def.Name] = t
	}
	return r
}

// AddOutside registers one tool of an outside server under "<server>.<tool>".
func (r *Registry) AddOutside(def llm.ToolDef, run func(ctx context.Context, args json.RawMessage) (string, string)) {
	r.outside[def.Name] = outsideTool{def: def, run: run}
}

// KnownTools returns the names of the built-in tools, the engine hands it to the Configuration Manager as the known tools.
func KnownTools() map[string]bool {
	out := map[string]bool{}
	for _, t := range builtinTools() {
		out[t.def.Name] = true
	}
	return out
}

// Bound is the registry narrowed to one session's tools and guard.
type Bound struct {
	reg   *Registry
	names []string
	guard *Guard
	calls atomic.Int64
	logMu sync.Mutex
}

// Bind attaches the permitted tools to one session and fails on a permitted tool that is not registered.
func (r *Registry) Bind(permitted []string, g *Guard) (*Bound, error) {
	if g == nil {
		return nil, fmt.Errorf("bind tools: no guard")
	}
	for _, n := range permitted {
		_, built := r.tools[n]
		_, out := r.outside[n]
		if !built && !out {
			return nil, fmt.Errorf("the permitted tool %q is not registered", n)
		}
	}
	return &Bound{reg: r, names: slices.Clone(permitted), guard: g}, nil
}

// Advertised returns the names of the tools the session may see.
func (b *Bound) Advertised() []string { return slices.Clone(b.names) }

// Definition returns the definition of one advertised tool.
func (b *Bound) Definition(name string) (llm.ToolDef, bool) {
	if !slices.Contains(b.names, name) {
		return llm.ToolDef{}, false
	}
	if t, ok := b.reg.tools[name]; ok {
		return t.def, true
	}
	if o, ok := b.reg.outside[name]; ok {
		return o.def, true
	}
	return llm.ToolDef{}, false
}

// Guard returns the guard the tools run behind.
func (b *Bound) Guard() *Guard { return b.guard }

// Dispatch runs the guard, then the tool, and returns a capped reply; a refusal is a reply.
func (b *Bound) Dispatch(ctx context.Context, name string, args json.RawMessage) (reply Reply) {
	env := b.reg.env
	id := "c" + strconv.FormatInt(b.calls.Add(1), 10)
	b.line(ToolLine{Kind: "start", Call: id, Tool: name})
	v := b.guard.Check(Call{Tool: name, Args: args})
	if !v.Allowed {
		data, _ := json.Marshal(map[string]any{"tool": name, "row": v.Row, "target": v.Target, "count": v.Count})
		b.line(ToolLine{Kind: "denied", Call: id, Tool: name, Data: data})
		b.fact(Fact{Subtype: "tool.denied", At: env.now(), Data: data})
		text := v.Message + " " + v.Alternative
		if v.EndSession {
			text += " This session is over."
		}
		return Reply{Text: text}
	}
	outcome := "ok"
	defer func() {
		if r := recover(); r != nil {
			reply, outcome = Reply{Text: fmt.Sprintf("error: the tool failed unexpectedly: %v", r)}, "error"
		}
		if strings.HasPrefix(reply.Text, timeoutLine) {
			outcome = "timeout"
		} else if strings.HasPrefix(reply.Text, "error:") {
			outcome = "error"
		}
		reply = b.cap(id, name, reply)
		b.line(ToolLine{Kind: "end", Call: id, Tool: name, Outcome: outcome})
	}()
	if t, ok := b.reg.tools[name]; ok {
		return t.run(ctx, &toolCall{env: env, guard: b.guard, bound: b, id: id}, args)
	}
	if o, ok := b.reg.outside[name]; ok {
		text, res := o.run(ctx, args)
		outcome = res
		server, tn, _ := strings.Cut(name, ".")
		data, _ := json.Marshal(map[string]string{"server": server, "tool": tn, "outcome": res})
		b.fact(Fact{Subtype: "tool.outside", At: env.now(), Data: data})
		return Reply{Text: text}
	}
	return Reply{Text: "error: the tool " + strconv.Quote(name) + " is not registered"}
}

// cap keeps a reply within 4 KB by writing the whole text to out/<call>.log; read is never capped.
func (b *Bound) cap(id, name string, r Reply) Reply {
	if name == "read" || r.LogPath != "" || len(r.Text) <= replyCap {
		return r
	}
	dir := b.reg.env.Dir
	if dir == "" {
		r.Text = capText(r.Text, "(not kept)")
		return r
	}
	logPath := filepath.Join(dir, "out", id+".log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err == nil {
		if err = os.WriteFile(logPath, []byte(r.Text), 0o644); err == nil {
			return Reply{Text: capText(r.Text, logPath), LogPath: logPath}
		}
	}
	return Reply{Text: capText(r.Text, "(not kept)")}
}

// ToolLine is one line of tools.jsonl.
type ToolLine struct {
	Kind    string          `json:"kind"`              // start, end, denied or finding
	Call    string          `json:"call"`              // the tool call identifier, shared by its start and end lines
	Tool    string          `json:"tool"`              // the tool name, such as edit
	At      time.Time       `json:"at"`                // when the tool server saw it, whole seconds
	Outcome string          `json:"outcome,omitempty"` // end only: ok, error or timeout
	Data    json.RawMessage `json:"data,omitempty"`    // denied: the tool, the row, the target and the count; finding: rule, file, line, quote, note
}

// line appends one ToolLine to tools.jsonl in the session folder.
func (b *Bound) line(l ToolLine) {
	env := b.reg.env
	if env.Dir == "" {
		return
	}
	l.At = env.now().Truncate(time.Second)
	raw, err := json.Marshal(l)
	if err != nil {
		return
	}
	b.logMu.Lock()
	defer b.logMu.Unlock()
	f, err := os.OpenFile(filepath.Join(env.Dir, "tools.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.Write(append(raw, '\n'))
}

// fact hands a fact to the environment's receiver.
func (b *Bound) fact(f Fact) {
	if b.reg.env.OnFact != nil {
		b.reg.env.OnFact(f)
	}
}

// def builds a tool definition from a schema written as JSON text.
func def(name, desc, schema string) llm.ToolDef {
	return llm.ToolDef{Name: name, Description: desc, InputSchema: json.RawMessage(schema)}
}

// builtinTools lists the tools every session can be given.
func builtinTools() []tool {
	return []tool{
		{def("read", "Read a file. Lines count from 1; start and end are optional. At most 2000 lines come back per call.",
			`{"type":"object","properties":{"path":{"type":"string"},"start":{"type":"integer"},"end":{"type":"integer"}},"required":["path"],"additionalProperties":false}`), toolRead},
		{def("search", "Search files for a Go regular expression; path narrows the search to a folder.",
			`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"}},"required":["pattern"],"additionalProperties":false}`), toolSearch},
		{def("edit", "Replace one exact piece of text in a file with new text. The old text must occur exactly once.",
			`{"type":"object","properties":{"path":{"type":"string"},"old":{"type":"string"},"new":{"type":"string"}},"required":["path","old","new"],"additionalProperties":false}`), toolEdit},
		{def("write", "Create a new file. An existing file is refused; use edit for it.",
			`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"],"additionalProperties":false}`), toolWrite},
		{def("run", "Run a command class with extra arguments, inside the sandbox. No shell is involved.",
			`{"type":"object","properties":{"class":{"type":"string"},"args":{"type":"array","items":{"type":"string"}}},"required":["class"],"additionalProperties":false}`), toolRun},
		{def("diff", "Show the unified diff of the group's change against its base commit; paths narrows it.",
			`{"type":"object","properties":{"paths":{"type":"array","items":{"type":"string"}}},"additionalProperties":false}`), toolDiff},
		{def("git_read", "Run a read-only git command: log, show, blame or status.",
			`{"type":"object","properties":{"command":{"type":"string","enum":["log","show","blame","status"]},"args":{"type":"array","items":{"type":"string"}}},"required":["command"],"additionalProperties":false}`), toolGitRead},
		{def("skill", "Read a skill, or one section of it as skill#section.",
			`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"],"additionalProperties":false}`), toolSkill},
		{def("submit_finding", "File one review finding with the rule, the file, the line and the quoted line.",
			`{"type":"object","properties":{"rule":{"type":"string"},"file":{"type":"string"},"line":{"type":"integer"},"quote":{"type":"string"},"note":{"type":"string"}},"required":["rule","file","line","quote","note"],"additionalProperties":false}`), toolSubmitFinding},
		{def("task_done", "Report a task finished: the harness reruns its done_when command and returns the result.",
			`{"type":"object","properties":{"task":{"type":"string"}},"required":["task"],"additionalProperties":false}`), toolTaskDone},
		{def("lint_backlog", "Check a planned task list and return its lint errors, or ok.",
			`{"type":"object","properties":{"draft":{"type":"string"}},"required":["draft"],"additionalProperties":false}`), toolLintBacklog},
	}
}
