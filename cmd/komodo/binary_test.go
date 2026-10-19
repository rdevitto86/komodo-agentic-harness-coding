package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

// binaryPath is the komodo binary the tests in this file start as a child process; TestMain builds it.
var binaryPath string

// TestMain builds the binary once, so a test can start it the way Claude Code does.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "komodo-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, "make the build folder:", err)
		os.Exit(1)
	}

	binaryPath = filepath.Join(dir, "komodo")

	if out, err := exec.Command("go", "build", "-o", binaryPath, ".").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build the binary: %v\n%s", err, out)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}

	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// child starts the binary with a private home folder, standard input and the extra environment.
func child(t *testing.T, stdin string, env []string, args ...string) *exec.Cmd {
	t.Helper()

	home := t.TempDir()
	cmd := exec.Command(binaryPath, args...)
	cmd.Env = append([]string{
		"HOME=" + home,
		"KOMODO_HOME=" + filepath.Join(home, ".komodo"),
		"PATH=" + os.Getenv("PATH"),
	}, env...)
	cmd.Stdin = strings.NewReader(stdin)

	return cmd
}

// runChild runs the binary to its end and returns its exit code and both output streams.
func runChild(t *testing.T, cmd *exec.Cmd) (int, string, string) {
	t.Helper()

	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut

	err := cmd.Run()
	code := 0

	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run the binary: %v", err)
	}

	return code, out.String(), errOut.String()
}

// mcpEntry renders the mcp.json of a session and returns the command line and environment it holds.
func mcpEntry(t *testing.T, worktree string) (args, env []string) {
	t.Helper()

	cfg := task.ToolServerConfig{
		Agent: "reviewer", Session: "s-1", Worktree: worktree, Dir: t.TempDir(), Commands: map[string][]string{},
		Permissions:  task.Permissions{Tools: []string{"read", "search"}},
		RefusalLimit: 3,
	}

	raw, err := task.MCPConfig(binaryPath, cfg)
	if err != nil {
		t.Fatal(err)
	}

	var doc struct {
		MCPServers map[string]struct {
			Command string
			Args    []string
			Env     map[string]string
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("mcp.json = %s (%v)", raw, err)
	}

	e := doc.MCPServers["komodo"]
	for k, v := range e.Env {
		env = append(env, k+"="+v)
	}

	return e.Args, env
}

func TestToolsBinaryAnswersInitializeAndToolsList(t *testing.T) {
	worktree, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	args, env := mcpEntry(t, worktree)
	cmd := child(t, "", env, args...)
	cmd.Stdin = nil

	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}

	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}

	var errOut bytes.Buffer
	cmd.Stderr = &errOut

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	out := bufio.NewReader(outPipe)
	ask := func(line string) map[string]any {
		t.Helper()

		if _, err := fmt.Fprintln(in, line); err != nil {
			t.Fatalf("write to the tool server: %v\nstderr=%s", err, errOut.String())
		}

		reply, err := out.ReadString('\n')
		if err != nil {
			t.Fatalf("read the reply: %v\nstderr=%s", err, errOut.String())
		}

		var m map[string]any
		if err := json.Unmarshal([]byte(reply), &m); err != nil {
			t.Fatalf("reply %q is not JSON: %v", reply, err)
		}

		return m
	}

	started := time.Now()
	init := ask(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)

	if res, _ := init["result"].(map[string]any); res == nil || res["protocolVersion"] != "2025-03-26" {
		t.Fatalf("initialize = %v", init)
	}
	if d := time.Since(started); d > 5*time.Second {
		t.Errorf("initialize took %s, the program waits 30 s", d)
	}

	list, _ := ask(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)["result"].(map[string]any)
	tools, _ := list["tools"].([]any)

	var names []string
	for _, tl := range tools {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "read,search" {
		t.Fatalf("tools/list = %v", names)
	}

	call := ask(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read","arguments":{"path":"a.txt"}}}`)
	if res, _ := call["result"].(map[string]any); res == nil || res["isError"] != false ||
		!strings.Contains(fmt.Sprint(res["content"]), "hello") {
		t.Fatalf("read call = %v", call)
	}

	call = ask(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"read","arguments":{"path":"/etc/passwd"}}}`)
	if res, _ := call["result"].(map[string]any); res == nil || res["isError"] != true ||
		!strings.Contains(fmt.Sprint(res["content"]), "Refused by G2") {
		t.Fatalf("a read outside the worktree = %v", call)
	}

	_ = in.Close()

	if err := cmd.Wait(); err != nil {
		t.Fatalf("the tool server must exit 0 when its input ends: %v\nstderr=%s", err, errOut.String())
	}
}

func TestToolsBinaryWithoutAgentFails(t *testing.T) {
	code, out, errOut := runChild(t, child(t, "", nil, "internal", "tools"))
	if code != 4 || out != "" || !strings.Contains(errOut, "--agent and --session") {
		t.Fatalf("exit %d\nstdout=%q\nstderr=%q", code, out, errOut)
	}
}

func TestHiddenVerbsAnswerHelpJSON(t *testing.T) {
	for _, verb := range []string{"engine", "tools"} {
		code, out, errOut := runChild(t, child(t, "", nil, "internal", verb, "--help", "--json"))
		if code != 0 || !strings.Contains(out, `"name": "internal `+verb+`"`) {
			t.Errorf("internal %s --help --json: exit %d\nstdout=%q\nstderr=%q", verb, code, out, errOut)
		}
	}
}
