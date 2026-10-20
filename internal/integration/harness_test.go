//go:build !windows

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/config"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The helpers in this file drive the real komodo binary against a temporary repository, a temporary home
// folder and a fake model server; nothing reaches the real home, the real claude program or the network.

// binOnce builds the komodo binary once per test run.
var (
	binOnce sync.Once
	binPath string
	binErr  error
)

// komodoBinary builds ./cmd/komodo into a temporary folder and returns its path.
func komodoBinary(t *testing.T) string {
	t.Helper()
	binOnce.Do(func() {
		dir, err := os.MkdirTemp("", "kbin")
		if err != nil {
			binErr = err
			return
		}
		binPath = filepath.Join(dir, "komodo")
		cmd := exec.Command("go", "build", "-o", binPath, "./cmd/komodo")
		cmd.Dir = filepath.Join(mustWd(), "..", "..")
		if out, err := cmd.CombinedOutput(); err != nil {
			binErr = fmt.Errorf("go build ./cmd/komodo: %v\n%s", err, out)
			return
		}
		binErr = startWatchdog(os.Getpid(), binPath)
	})
	if binErr != nil {
		t.Fatal(binErr)
	}
	return binPath
}

// startWatchdog starts a detached shell that waits for process pid to end, then kills every engine started from bin and
// removes bin's folder. Cleanups do not run when a test run times out or is killed, so they cannot stop the engines.
func startWatchdog(pid int, bin string) error {
	const script = `while kill -0 "$1" 2>/dev/null; do sleep 1; done; pkill -f -- "$2 internal engine"; rm -rf "$3"`
	cmd := exec.Command("sh", "-c", script, "sh", strconv.Itoa(pid), bin, filepath.Dir(bin))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the engine watchdog: %w", err)
	}
	return cmd.Process.Release()
}

// mustWd returns the test's working folder, which is the package folder.
func mustWd() string {
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return wd
}

// inRepo reports whether dir or a parent holds a .git entry.
func inRepo(dir string) (string, bool) {
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d, true
		}
		if filepath.Dir(d) == d {
			return "", false
		}
	}
}

// binRig is one binary, one home folder, one fixture repository and one fake model.
type binRig struct {
	t      *testing.T
	root   string // temporary folder holding everything below
	home   string // KOMODO_HOME
	repo   string // the fixture working repository
	origin string // the bare repository the fixture pushes to
	bin    string // the komodo binary
	ghLog  string // file the fake gh appends its arguments to
	model  *fakeModel
	env    []string
}

// newBinRig builds the folders, the fixture repository with the given extra files, the fake gh and the fake model.
func newBinRig(t *testing.T, extra map[string]string) *binRig {
	t.Helper()
	root, err := os.MkdirTemp("", "k")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if r, bad := inRepo(root); bad {
		t.Fatalf(
			"the temporary folder %s sits inside the repository %s: set TMPDIR to a folder outside every checkout",
			root,
			r,
		)
	}
	r := &binRig{t: t, root: root, home: filepath.Join(
		root,
		"h",
	), repo: filepath.Join(root, "work"), origin: filepath.Join(root, "origin.git"),
		ghLog: filepath.Join(root, "gh.log"), bin: komodoBinary(t)}
	if n := len(filepath.Join(r.home, "state", "engine.sock")); n > 100 {
		t.Fatalf("the socket path is %d bytes; set TMPDIR to a shorter folder", n)
	}
	for _, d := range []string{r.home, r.repo, filepath.Join(root, "bin"), filepath.Join(root, "home")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r.writeGH()
	r.model = newFakeModel(t)
	r.env = []string{
		"KOMODO_HOME=" + r.home, "HOME=" + filepath.Join(root, "home"),
		"PATH=" + filepath.Join(root, "bin") + string(os.PathListSeparator) + os.Getenv("PATH"),
		"ANTHROPIC_API_KEY=test-key",
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_NOSYSTEM=1", "NO_COLOR=1",
	}
	r.initRepo(extra)
	t.Cleanup(r.killEngine)
	cfg, err := config.New(
		r.home,
		config.Known{GuardRows: task.KnownRows(), Tools: task.KnownTools()},
		func(string) bool { return true },
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.SetConfigItem(config.Change{Key: "providers.paid", Old: "never", New: "allowed"}); err != nil {
		t.Fatalf("allow the paid link: %v", err)
	}
	if _, err := cfg.SetConfigItem(config.Change{Key: "providers.anthropic.url", New: r.model.srv.URL}); err != nil {
		t.Fatalf("point the paid link at the fake model: %v", err)
	}
	return r
}

// writeGH installs a fake gh that records its arguments and prints what the Commit Handler reads.
func (r *binRig) writeGH() {
	script := "#!/bin/sh\necho \"$@\" >> '" + r.ghLog + "'\n" +
		"case \"$1 $2\" in\n  \"pr list\") echo '[]';;\n  \"pr create\") echo " +
		"'https://example.invalid/pull/1';;\nesac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(r.root, "bin", "gh"), []byte(script), 0o755); err != nil {
		r.t.Fatal(err)
	}
}

// git runs git in dir with the test identity and returns its output.
func (r *binRig) git(dir string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = r.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// baseFiles is the tiny Go module every fixture starts from.
var baseFiles = map[string]string{
	"go.mod":       "module example.com/tiny\n\ngo 1.27\n",
	"tiny/tiny.go": "// Package tiny is a fixture.\npackage tiny\n\n// One returns 1.\nfunc One() int { return 1 }\n",
	"tiny/tiny_test.go": "package tiny\n\nimport \"testing\"\n\nfunc TestOne(t *testing.T) {\n\tif One() != 1 " +
		"{\n\t\tt.Fatal(\"one\")\n\t}\n}\n",
	"README.md": "# tiny\n",
}

// initRepo creates the working repository with one commit, the bare origin and the first push.
func (r *binRig) initRepo(extra map[string]string) {
	r.git(r.root, "init", "-q", "--bare", "-b", "main", r.origin)
	r.git(r.repo, "init", "-q", "-b", "main")
	r.git(r.repo, "config", "user.name", "Test")
	r.git(r.repo, "config", "user.email", "test@example.com")
	r.git(r.repo, "config", "commit.gpgsign", "false")
	sets := []map[string]string{baseFiles, extra}
	if _, own := extra["go.mod"]; own {
		sets = sets[1:]
	}
	for _, files := range sets {
		for p, body := range files {
			full := filepath.Join(r.repo, filepath.FromSlash(p))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				r.t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
				r.t.Fatal(err)
			}
		}
	}
	r.git(r.repo, "add", "-A")
	r.git(r.repo, "commit", "-q", "-m", "initial")
	r.git(r.repo, "remote", "add", "origin", r.origin)
	r.git(r.repo, "push", "-q", "-u", "origin", "main")
}

// result is the outcome of one komodo command.
type result struct {
	code   int
	stdout string
	stderr string
}

// komodo runs the binary in the repository with the binRig's environment and a timeout.
func (r *binRig) komodo(timeout time.Duration, args ...string) result {
	r.t.Helper()
	cmd := exec.Command(r.bin, args...)
	cmd.Dir = r.repo
	cmd.Env = r.env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Start(); err != nil {
		r.t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			r.t.Fatal(err)
		}
		return result{code, out.String(), errb.String()}
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		r.t.Fatalf(
			"komodo %s did not finish in %s\nstdout: %s\nstderr: %s",
			strings.Join(args, " "),
			timeout,
			out.String(),
			errb.String(),
		)
		return result{}
	}
}

// killEngine ends the engine the CLI started, found through its heartbeat file.
func (r *binRig) killEngine() { r.signalEngine(syscall.SIGKILL) }

// signalEngine sends a signal to the engine process.
func (r *binRig) signalEngine(sig syscall.Signal) {
	b, err := os.ReadFile(filepath.Join(r.home, "state", "engine.beat"))
	if err != nil {
		return
	}
	var beat struct {
		PID int `json:"pid"`
	}
	if json.Unmarshal(b, &beat) == nil && beat.PID > 1 {
		_ = syscall.Kill(beat.PID, sig)
	}
}

// event is one recorded event with its data left raw.
type event struct {
	Type    string          `json:"type"`
	Subtype string          `json:"subtype"`
	Group   string          `json:"group"`
	Data    json.RawMessage `json:"data"`
}

// events reads every event line the engine wrote: the machine folder and each run's folder that runs.json names.
func (r *binRig) events() []event {
	var out []event
	dirs := []string{filepath.Join(r.home, "events")}
	if b, err := os.ReadFile(filepath.Join(r.home, "events", "runs.json")); err == nil {
		var m map[string]string
		if json.Unmarshal(b, &m) == nil {
			for _, d := range m {
				if !slices.Contains(dirs, d) {
					dirs = append(dirs, d)
				}
			}
		}
	}
	for _, dir := range dirs {
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || filepath.Base(p) == "runs.json" {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			for _, line := range strings.Split(string(b), "\n") {
				var e event
				if strings.HasPrefix(line, "{") && json.Unmarshal([]byte(line), &e) == nil && e.Subtype != "" {
					out = append(out, e)
				}
			}
			return nil
		})
	}
	return out
}

// has reports whether an event of the type and subtype exists whose data contains want.
func has(evs []event, typ, sub, want string) bool {
	for _, e := range evs {
		if e.Type == typ && e.Subtype == sub && strings.Contains(string(e.Data), want) {
			return true
		}
	}
	return false
}

// stages returns the stage.enter targets in order.
func stages(evs []event) []string {
	var out []string
	for _, e := range evs {
		if e.Type == "run-status" && e.Subtype == "stage.enter" {
			var d struct {
				To string `json:"to"`
			}
			_ = json.Unmarshal(e.Data, &d)
			out = append(out, d.To)
		}
	}
	return out
}

// waitDone polls the event store until a group.done event appears; a blocked group or the timeout fails the test with the
// evidence.
func (r *binRig) waitDone(timeout time.Duration) []event {
	r.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		evs := r.events()
		// The run ends only after each quiet epic's one push, so the pushed branch and the pull request exist by then.
		if has(evs, "run-complete", "done", "") {
			return evs
		}
		if has(evs, "error", "run-refused", "") {
			r.t.Fatalf("the run was refused\n%s", r.evidence(evs))
		}
		if has(evs, "run-failure", "group.blocked", "") {
			r.t.Fatalf("the group blocked\n%s", r.evidence(evs))
		}
		time.Sleep(300 * time.Millisecond)
	}
	r.signalEngine(syscall.SIGUSR1)
	time.Sleep(time.Second)
	r.t.Fatalf("no group.done within %s\n%s", timeout, r.evidence(r.events()))
	return nil
}

// evidence renders the last events, the model calls and the engine log for a failure message.
func (r *binRig) evidence(evs []event) string {
	var b strings.Builder
	for _, e := range evs[max(0, len(evs)-40):] {
		fmt.Fprintf(&b, "%s/%s group=%s %s\n", e.Type, e.Subtype, e.Group, e.Data)
	}
	fmt.Fprintf(&b, "model calls: %v\n", r.model.callCounts())
	_ = filepath.WalkDir(r.home, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(r.home, p)
		if strings.HasPrefix(rel, "events") {
			c, _ := os.ReadFile(p)
			fmt.Fprintf(&b, "FILE %s:\n%s\n", rel, c)
		} else if filepath.Base(rel) == "messages.jsonl" {
			c, _ := os.ReadFile(p)
			for _, line := range strings.Split(string(c), "\n") {
				if i := strings.Index(line, "tool_result"); i >= 0 {
					fmt.Fprintf(&b, "RESULT %s\n", line[i:min(len(line), i+500)])
				}
			}
		} else if filepath.Base(rel) == "tools.jsonl" {
			c, _ := os.ReadFile(p)
			if len(c) > 3000 {
				c = c[len(c)-3000:]
			}
			fmt.Fprintf(&b, "TOOLS %s:\n%s\n", rel, c)
		} else if !strings.HasPrefix(rel, "cache") {
			fmt.Fprintf(&b, "FILE %s\n", rel)
		}
		return nil
	})
	if logs, _ := filepath.Glob(filepath.Join(r.home, "state", "*.log")); len(logs) > 0 {
		for _, l := range logs {
			c, _ := os.ReadFile(l)
			fmt.Fprintf(&b, "%s:\n%s\n", l, c)
		}
	}
	return b.String()
}

// ghCalls returns the lines the fake gh recorded.
func (r *binRig) ghCalls() string {
	b, _ := os.ReadFile(r.ghLog)
	return string(b)
}

// planFiles is the one-epic, one-group task list the fake planner returns and phase 5 commits in advance.
func planFiles() map[string]string {
	return map[string]string{
		"docs/backlog/epic-1/tg-1.1/TG.md": "id: 0192e4c5-7a10-7d3e-9b2f-000000000101\ngoal: Add an Add function to the " +
			"tiny package\nsubsystem: none\n" +
			"priority: normal\npoints: 3\nstatus: todo\nfiles:\n- tiny/add.go\ndependencies:\n\n" +
			"- WHEN Add is called THE SYSTEM SHALL return the sum of its arguments\n  cmd: go test ./...\n",
		"docs/backlog/epic-1/tg-1.1/tsk-add.md": "files:\n- tiny/add.go\ndone_when: test -f tiny/add.go\nestimate_lines: " +
			"10\n\nAdd func Add(a, b int) int to the tiny package.\n",
	}
}

// --- fake model

// fakeModel replays the Messages interface for the planner, builder, tester and reviewer.
type fakeModel struct {
	t     *testing.T
	srv   *httptest.Server
	mu    sync.Mutex
	calls map[string]int

	hold    chan struct{} // when set, the builder's model calls wait until it closes
	started chan struct{} // closed at the first builder call while hold is set
	once    sync.Once

	plan     map[string]string                                     // when set, the planner returns these files
	builder  func(brief string, turn int) (string, map[string]any) // when set, picks the builder's tool call; "" ends the turn
	verdict  func(task string) string                              // when set, the reviewer's verdict per task
	testPass func(item string) bool                                // when set, the tester's pass flag per free item
	bodies   []string                                              // every request body, in arrival order

	builderFinal func() string // when set and not empty, the builder's final result
}

// newFakeModel starts the server and stops it with the test.
func newFakeModel(t *testing.T) *fakeModel {
	f := &fakeModel{t: t, calls: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// callCounts returns the number of requests per role.
func (f *fakeModel) callCounts() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]int{}
	for k, v := range f.calls {
		out[k] = v
	}
	return out
}

// serve answers the model list and the message calls.
func (f *fakeModel) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/v1/models":
		_, _ = io.WriteString(
			w,
			`{"data":[{"id":"claude-opus-5-5","max_input_tokens":1000000},{"id":"claude-sonnet-5",`+
				`"max_input_tokens":900000},{"id":"claude-haiku-5-5","max_input_tokens":800000}]}`,
		)
	case strings.HasPrefix(r.URL.Path, "/v1/models/"):
		_, _ = io.WriteString(w, `{"id":"x","max_input_tokens":777000}`)
	case r.URL.Path == "/v1/messages":
		b, _ := io.ReadAll(r.Body)
		f.messages(w, b)
	default:
		http.NotFound(w, r)
	}
}

// apiBody is the part of a request body the fake reads.
type apiBody struct {
	Tools []struct {
		Name string `json:"name"`
	} `json:"tools"`
	Messages []struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"messages"`
	OutputConfig *struct {
		Format *json.RawMessage `json:"format"`
	} `json:"output_config"`
}

var (
	taskLine = regexp.MustCompile(`(?m)^- (\S+): .*\(done when: `)
	itemLine = regexp.MustCompile(`(?m)^- (\S+): .*\(command: `)
)

// messages picks the reply by the agent's tools and by the turn.
func (f *fakeModel) messages(w http.ResponseWriter, raw []byte) {
	var b apiBody
	if err := json.Unmarshal(raw, &b); err != nil {
		http.Error(w, `{"error":{"type":"invalid_request_error","message":"bad body"}}`, 400)
		return
	}
	tools := map[string]bool{}
	for _, t := range b.Tools {
		tools[t.Name] = true
	}
	role := "reviewer"
	switch {
	case tools["lint_backlog"]:
		role = "planner"
	case tools["write"] || tools["edit"]:
		role = "builder"
	case tools["submit_finding"] || tools["skill"]:
		role = "reviewer"
	case tools["run"]:
		role = "tester"
	}
	f.mu.Lock()
	hold, started := f.hold, f.started
	f.mu.Unlock()
	if role == "builder" && hold != nil {
		f.once.Do(func() { close(started) })
		select {
		case <-hold:
		case <-time.After(90 * time.Second):
		}
	}
	final := b.OutputConfig != nil && b.OutputConfig.Format != nil
	assistant := 0
	brief := ""
	for i, m := range b.Messages {
		if m.Role == "assistant" {
			assistant++
		}
		if i == 0 {
			for _, c := range m.Content {
				brief += c.Text + "\n"
			}
		}
	}
	f.mu.Lock()
	f.calls[role]++
	f.bodies = append(f.bodies, string(raw))
	f.mu.Unlock()
	if final {
		sseText(w, f.result(role, brief))
		return
	}
	if role == "builder" && f.builder != nil {
		if name, input := f.builder(brief, assistant); name != "" {
			sseTool(w, fmt.Sprintf("tu-%d", assistant), name, input)
			return
		}
		sseText(w, "the work is done")
		return
	}
	if role == "builder" {
		switch assistant {
		case 0:
			sseTool(w, "tu-write", "write", map[string]any{"path": "tiny/add.go",
				"content": "package tiny\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int { return a + b }\n"})
			return
		case 1:
			sseTool(w, "tu-test", "write", map[string]any{"path": "tiny/add_test.go",
				"content": "package tiny\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 " +
					"{\n\t\tt.Fatal(\"add\")\n\t}\n}\n"})
			return
		case 2:
			sseTool(w, "tu-done", "task_done", map[string]any{"task": firstMatch(taskLine, brief)})
			return
		}
	}
	sseText(w, "the work is done")
}

// firstMatch returns the first capture of re in s.
func firstMatch(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// result renders the final JSON of a role; ids are read from the brief the Task Manager built.
func (f *fakeModel) result(role, brief string) string {
	switch role {
	case "planner":
		files := []map[string]string{}
		plan := f.plan
		if plan == nil {
			plan = planFiles()
		}
		for p, c := range plan {
			files = append(files, map[string]string{"path": strings.TrimPrefix(p, "docs/backlog/"), "content": c})
		}
		b, _ := json.Marshal(map[string]any{"files": files})
		return string(b)
	case "builder":
		if f.builderFinal != nil {
			if s := f.builderFinal(); s != "" {
				return s
			}
		}
		return `{"status":"done","summary":"added the Add function"}`
	case "tester":
		items, tasks := []map[string]any{}, []map[string]string{}
		for _, m := range itemLine.FindAllStringSubmatch(brief, -1) {
			pass := f.testPass == nil || f.testPass(m[1])
			items = append(items, map[string]any{"item": m[1], "pass": pass, "evidence": "ok"})
		}
		for _, m := range taskLine.FindAllStringSubmatch(brief, -1) {
			tasks = append(tasks, map[string]string{"task": m[1], "confirmation": "confirmed"})
		}
		b, _ := json.Marshal(map[string]any{"items": items, "tasks": tasks})
		return string(b)
	}
	vs := []map[string]string{}
	for _, m := range taskLine.FindAllStringSubmatch(brief, -1) {
		v := "met"
		if f.verdict != nil {
			v = f.verdict(m[1])
		}
		vs = append(vs, map[string]string{"task": m[1], "verdict": v})
	}
	b, _ := json.Marshal(map[string]any{"verdicts": vs})
	return string(b)
}

// sseEvent writes one server-sent event.
func sseEvent(w http.ResponseWriter, name string, data any) {
	b, _ := json.Marshal(data)
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
}

// sseHead writes the stream header and the opening event.
func sseHead(w http.ResponseWriter) {
	w.Header().Set("content-type", "text/event-stream")
	sseEvent(w, "message_start", map[string]any{
		"type":    "message_start",
		"message": map[string]any{"usage": map[string]any{"input_tokens": 30, "output_tokens": 1}},
	})
}

// sseText streams a text-only reply that ends the turn.
func sseText(w http.ResponseWriter, text string) {
	sseHead(w)
	sseEvent(w, "content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         0,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
	sseEvent(w, "content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": 0,
		"delta": map[string]any{"type": "text_delta", "text": text},
	})
	sseEvent(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	sseEvent(w, "message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn"},
		"usage": map[string]any{"output_tokens": 12},
	})
	sseEvent(w, "message_stop", map[string]any{"type": "message_stop"})
}

// sseTool streams a reply that calls one tool.
func sseTool(w http.ResponseWriter, id, name string, input map[string]any) {
	sseHead(w)
	sseEvent(w, "content_block_start", map[string]any{"type": "content_block_start", "index": 0,
		"content_block": map[string]any{"type": "tool_use", "id": id, "name": name, "input": map[string]any{}}})
	args, _ := json.Marshal(input)
	sseEvent(w, "content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": 0,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": string(args)},
	})
	sseEvent(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	sseEvent(w, "message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "tool_use"},
		"usage": map[string]any{"output_tokens": 9},
	})
	sseEvent(w, "message_stop", map[string]any{"type": "message_stop"})
}

// arm makes the builder's model calls wait until the returned release function runs; started closes at the first such call.
func (f *fakeModel) arm() (started <-chan struct{}, release func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hold, f.started = make(chan struct{}), make(chan struct{})
	hold := f.hold
	return f.started, func() { close(hold) }
}

// writeDocker puts a fake docker on PATH that keeps one file per container in a folder and understands ps and rm and has no daemon, so the Tester falls back; it returns
// the folder.
func (r *binRig) writeDocker() string {
	dir := filepath.Join(r.root, "containers")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.t.Fatal(err)
	}
	script := "#!/bin/sh\nD=" + dir +
		"\ncase \"$1\" in\nversion) exit 1;;\nps)\n  pat=\"\"\n  while [ $# -gt 0 ]; do case \"$1\" in --filter) shift; pat=${1#name=};; " +
		"esac; shift; done\n" +

		"  for f in \"$D\"/*; do [ -e \"$f\" ] || continue; n=$(basename \"$f\"); echo \"$n\" | grep -Eq \"$pat\" && echo " +
		"\"$n\"; done;;\n" +
		"rm) shift; [ \"$1\" = -f ] && shift; rm -f \"$D/$1\";;\nesac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(r.root, "bin", "docker"), []byte(script), 0o755); err != nil {
		r.t.Fatal(err)
	}
	return dir
}

// writeLiveDocker puts a fake docker on PATH whose daemon answers: create makes one file per container, exec runs the command
// on the host in the container's worktree, and rm and ps work as in writeDocker. It returns the container folder and a log of
// every create.
func (r *binRig) writeLiveDocker() (dir, made string) {
	dir = filepath.Join(r.root, "containers")
	made = filepath.Join(r.root, "docker-created.log")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.t.Fatal(err)
	}
	script := `#!/bin/sh
D=` + dir + `
case "$1" in
version|start|network) exit 0;;
create)
  shift; name=""; wt=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --name) shift; name=$1;;
      -v) shift; case "$1" in *:/work) wt=${1%:/work};; esac;;
    esac
    shift
  done
  echo "$wt" > "$D/$name"; echo "create $name" >> '` + made + `'; echo "$name";;
exec)
  shift
  while [ $# -gt 0 ]; do
    case "$1" in
      -i) shift;;
      -w|-u) shift 2;;
      -e) export "$2"; shift 2;;
      *) break;;
    esac
  done
  name=$1; shift
  if [ "$1" = timeout ]; then shift 4; fi
  wt=$(cat "$D/$name") || exit 1
  cd "$wt" && exec "$@";;
ps)
  pat=""
  while [ $# -gt 0 ]; do case "$1" in --filter) shift; pat=${1#name=};; esac; shift; done
  for f in "$D"/*; do [ -e "$f" ] || continue; n=$(basename "$f"); echo "$n" | grep -Eq "$pat" && echo "$n"; done;;
rm) shift; [ "$1" = -f ] && shift; rm -f "$D/$1";;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(r.root, "bin", "docker"), []byte(script), 0o755); err != nil {
		r.t.Fatal(err)
	}
	return dir, made
}
