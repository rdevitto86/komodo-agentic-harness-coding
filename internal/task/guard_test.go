package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rowsDigest pins the policy rows: an edit to policy.json must come with a new case below and a new digest here.
const rowsDigest = "ccb148e11cba345c54f2ad603e6831b1f2fc314f2d8076f315600729a94f4bc6"

// jsonArgs marshals an argument object.
func jsonArgs(t testing.TB, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fixture is a worktree with an outside folder beside it.
type fixture struct {
	Root    string
	Outside string
	Home    string
}

// newFixture builds a worktree holding source files, a .git pointer file, a secret file and symlinks that escape.
func newFixture(t *testing.T) fixture {
	t.Helper()
	base := t.TempDir()
	f := fixture{Root: filepath.Join(base, "wt"), Outside: filepath.Join(base, "outside"), Home: filepath.Join(base, "home")}
	for _, d := range []string{f.Root, f.Outside, f.Home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		".git":                      "gitdir: " + filepath.Join(base, "repo.git") + "\n",
		"internal/lease/clock.go":   "package lease\n",
		"internal/lease/other.go":   "package lease\n",
		"cmd/main.go":               "package main\n",
		".env":                      "TOKEN=1\n",
		".env.example":              "TOKEN=\n",
		"certs.pem":                 "x\n",
		"bin/komodo":                "binary\n",
		"docs/prd.md":               "prd\n",
		".claude/settings.json":     "{}\n",
		"internal/task/policy.json": "{}\n",
		"README.md":                 "readme\n",
	}
	for p, c := range files {
		full := filepath.Join(f.Root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.Outside, "secret.txt"), []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		"escape":        f.Outside,                                // folder outside
		"escape.txt":    filepath.Join(f.Outside, "secret.txt"),   // file outside
		"dangling":      filepath.Join(f.Outside, "not-there.go"), // target missing, outside
		"inside":        filepath.Join(f.Root, "internal/lease"),  // folder inside
		"inside.go":     filepath.Join(f.Root, "cmd/main.go"),     // file inside
		"hop":           "escape",                                 // link to a link
		"rel-escape.go": "../outside/secret.txt",                  // relative escape
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(f.Root, name)); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// builderPerms is the builder's permissions as the registry lists them.
func builderPerms() Permissions {
	return Permissions{
		Tools:      []string{"read", "search", "edit", "write", "run", "skill", "task_done"},
		RunClasses: []string{"build", "test", "lint", "format"},
		Guard:      []RowID{"R1", "B1"},
	}
}

// reviewerPerms is the reviewer's permissions as the registry lists them.
func reviewerPerms() Permissions {
	return Permissions{
		Tools:      []string{"read", "search", "skill", "diff", "git_read", "run", "submit_finding"},
		RunClasses: []string{"build", "test", "lint"},
		Guard:      []RowID{"R1"},
	}
}

// newTestGuard compiles the shipped rows for one agent.
func newTestGuard(t *testing.T, f fixture, agent string, p Permissions, files []string, limit int) *Guard {
	t.Helper()
	rows, err := LoadRows(f.Home)
	if err != nil {
		t.Fatal(err)
	}
	g, err := NewGuard(rows, Binding{Agent: agent, Permissions: p, Worktree: f.Root, Files: files}, limit)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// guardCase is one call and the row that must refuse it; an empty row means allowed.
type guardCase struct {
	name string
	tool string
	args any
	row  string
}

// token builds a fake secret in pieces so the source holds no literal key.
func token(parts ...string) string { return strings.Join(parts, "") }

func TestGuardBuilderTable(t *testing.T) {
	f := newFixture(t)
	files := []string{"internal/lease/**", "docs/guide.md"}
	cases := []guardCase{
		{"read inside", "read", map[string]any{"path": "internal/lease/clock.go"}, ""},
		{"read lines", "read", map[string]any{"path": "internal/lease/clock.go", "start": 1, "end": 5}, ""},
		{"read absolute inside", "read", map[string]any{"path": filepath.Join(f.Root, "README.md")}, ""},
		{"read dotdot", "read", map[string]any{"path": "../outside/secret.txt"}, "G2"},
		{"read dotdot hidden in the middle", "read", map[string]any{"path": "internal/../../outside/secret.txt"}, "G2"},
		{"read absolute outside", "read", map[string]any{"path": "/etc/passwd"}, "G2"},
		{"read empty path", "read", map[string]any{"path": ""}, "G2"},
		{"read through a folder link", "read", map[string]any{"path": "escape/secret.txt"}, "G2"},
		{"read a file link", "read", map[string]any{"path": "escape.txt"}, "G2"},
		{"read a link to a link", "read", map[string]any{"path": "hop/secret.txt"}, "G2"},
		{"read a relative escape link", "read", map[string]any{"path": "rel-escape.go"}, "G2"},
		{"read a dangling link", "read", map[string]any{"path": "dangling"}, "G2"},
		{"read link to a folder inside", "read", map[string]any{"path": "inside/clock.go"}, ""},
		{"read link to a file inside", "read", map[string]any{"path": "inside.go"}, ""},
		{"read secret file", "read", map[string]any{"path": ".env"}, "G4"},
		{"read secret file upper case", "read", map[string]any{"path": ".ENV"}, "G4"},
		{"read example env", "read", map[string]any{"path": ".env.example"}, ""},
		{"read pem", "read", map[string]any{"path": "certs.pem"}, "G4"},
		{"read private key name", "read", map[string]any{"path": "keys/id_ed25519"}, "G4"},
		{"read key file", "read", map[string]any{"path": "deploy/tls.key"}, "G4"},
		{"search whole tree", "search", map[string]any{"pattern": "Lease"}, ""},
		{"search folder", "search", map[string]any{"pattern": "Lease", "path": "internal"}, ""},
		{"search outside", "search", map[string]any{"pattern": "x", "path": "../outside"}, "G2"},
		{"search through a link", "search", map[string]any{"pattern": "x", "path": "escape"}, "G2"},
		{"edit in files", "edit", map[string]any{"path": "internal/lease/clock.go", "old": "a", "new": "b"}, ""},
		{"edit outside files", "edit", map[string]any{"path": "cmd/main.go", "old": "a", "new": "b"}, "B1"},
		{"edit test sibling of a listed folder", "write", map[string]any{"path": "internal/lease/clock_test.go", "content": "package lease\n"}, ""},
		{"edit through a link out", "edit", map[string]any{"path": "escape/secret.txt", "old": "a", "new": "b"}, "G2"},
		{"write new file in files", "write", map[string]any{"path": "internal/lease/new.go", "content": "package lease\n"}, ""},
		{"write new file through a missing folder", "write", map[string]any{"path": "internal/lease/sub/new.go", "content": "package lease\n"}, ""},
		{"write through a link folder", "write", map[string]any{"path": "escape/new.go", "content": "x"}, "G2"},
		{"write through a dangling link", "write", map[string]any{"path": "dangling", "content": "x"}, "G2"},
		{"write .git pointer", "write", map[string]any{"path": ".git", "content": "gitdir: /tmp/evil"}, "G3"},
		{"edit .git pointer", "edit", map[string]any{"path": ".git", "old": "a", "new": "b"}, "G3"},
		{"write under .git", "write", map[string]any{"path": ".git/hooks/pre-commit", "content": "x"}, "G3"},
		{"write .git config", "write", map[string]any{"path": ".git/config", "content": "x"}, "G3"},
		{"write nested .git", "write", map[string]any{"path": "sub/.git/config", "content": "x"}, "G3"},
		{"write .GIT upper case", "write", map[string]any{"path": ".GIT/config", "content": "x"}, "G3"},
		{"write bin komodo", "edit", map[string]any{"path": "bin/komodo", "old": "a", "new": "b"}, "G3"},
		{"write claude settings", "edit", map[string]any{"path": ".claude/settings.json", "old": "a", "new": "b"}, "G3"},
		{"write the policy file", "edit", map[string]any{"path": "internal/task/policy.json", "old": "a", "new": "b"}, "G3"},
		{"write the prd", "edit", map[string]any{"path": "docs/prd.md", "old": "a", "new": "b"}, "G3"},
		{"edit a secret file", "edit", map[string]any{"path": ".env", "old": "a", "new": "b"}, "G4"},
		{"write private key content", "write", map[string]any{"path": "internal/lease/k.go", "content": "-----BEGIN RSA PRIVATE KEY-----\nabc"}, "G4"},
		{"write aws key", "write", map[string]any{"path": "internal/lease/k.go", "content": "id = " + token("AK", "IA", "ABCDEFGHIJKLMNOP")}, "G4"},
		{"write github token", "write", map[string]any{"path": "internal/lease/k.go", "content": token("gh", "p_", strings.Repeat("a", 30))}, "G4"},
		{"write anthropic key", "edit", map[string]any{"path": "internal/lease/clock.go", "old": "a", "new": token("sk-", "ant-", strings.Repeat("b", 30))}, "G4"},
		{"write slack token", "write", map[string]any{"path": "internal/lease/k.go", "content": token("xo", "xb-", "1234567890-abc")}, "G4"},
		{"write plain text near a prefix", "write", map[string]any{"path": "internal/lease/k.go", "content": "the ghp_ prefix and AKIA are documented\n"}, ""},
		{"write a co-author trailer", "write", map[string]any{"path": "internal/lease/n.md", "content": "fix\n\nCo-Authored-By: Claude <noreply@anthropic.com>\n"}, "G5"},
		{"write generated with line", "edit", map[string]any{"path": "internal/lease/clock.go", "old": "a", "new": "// Generated with Claude Code"}, "G5"},
		{"write a session link", "write", map[string]any{"path": "internal/lease/n.md", "content": "see https://claude.ai/code/session_1"}, "G5"},
		{"write a share link", "write", map[string]any{"path": "internal/lease/n.md", "content": "see https://claude.ai/share/abc"}, "G5"},
		{"write a human co-author", "write", map[string]any{"path": "internal/lease/n.md", "content": "Co-Authored-By: Ada <ada@example.com>\n"}, ""},
		{"run a listed class", "run", map[string]any{"class": "test", "args": []string{"./internal/lease/..."}}, ""},
		{"run without args", "run", map[string]any{"class": "build"}, ""},
		{"run an unlisted class", "run", map[string]any{"class": "deploy", "args": []string{}}, "R1"},
		{"run without a class", "run", map[string]any{"args": []string{"x"}}, "R1"},
		{"run with args that are not a list", "run", map[string]any{"class": "test", "args": "x"}, "R1"},
		{"run a shell word as a class", "run", map[string]any{"class": "test && rm -rf /", "args": []string{}}, "R1"},
		{"git_read for a builder", "git_read", map[string]any{"command": "log"}, "G1"},
		{"submit_finding for a builder", "submit_finding", map[string]any{"rule": "r", "file": "a.go", "line": 1, "quote": "q", "note": "n"}, "G1"},
		{"unknown tool", "bash", map[string]any{"command": "ls"}, "G1"},
		{"lint_backlog for a builder", "lint_backlog", map[string]any{"draft": "x"}, "G1"},
		{"skill", "skill", map[string]any{"name": "standards-go#errors"}, ""},
		{"skill with a path name", "skill", map[string]any{"name": "../../etc/passwd"}, "G1"},
		{"task_done", "task_done", map[string]any{"task": "t1"}, ""},
		{"unknown field", "read", map[string]any{"path": "README.md", "mode": "w"}, "G2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newTestGuard(t, f, "builder", builderPerms(), files, 100)
			v := g.Check(Call{Tool: c.tool, Args: jsonArgs(t, c.args)})
			checkVerdict(t, v, c.row)
		})
	}
}

// checkVerdict fails unless v is allowed (want empty) or refused by one of the rows in want, written as one or more ids.
func checkVerdict(t *testing.T, v Verdict, want string) {
	t.Helper()
	if want == "" {
		if !v.Allowed {
			t.Fatalf("refused by %s: %s", v.Row, v.Message)
		}
		return
	}
	if v.Allowed {
		t.Fatalf("allowed; want a refusal by %s", want)
	}
	if !strings.Contains(want, string(v.Row)) {
		t.Fatalf("refused by %s, want %s: %s", v.Row, want, v.Message)
	}
	if !strings.Contains(v.Message, string(v.Row)) || v.Alternative == "" {
		t.Fatalf("the refusal must name its row and give an alternative: %q / %q", v.Message, v.Alternative)
	}
}

func TestGuardReviewerAndPlanner(t *testing.T) {
	f := newFixture(t)
	cases := []guardCase{
		{"edit", "edit", map[string]any{"path": "README.md", "old": "a", "new": "b"}, "G1"},
		{"write", "write", map[string]any{"path": "x.go", "content": "x"}, "G1"},
		{"diff", "diff", map[string]any{"paths": []string{"internal/lease"}}, ""},
		{"diff outside", "diff", map[string]any{"paths": []string{"../outside"}}, "G2"},
		{"diff everything", "diff", map[string]any{}, ""},
		{"git_read status", "git_read", map[string]any{"command": "status"}, ""},
		{"git_read log with a count", "git_read", map[string]any{"command": "log", "args": []string{"-n", "5", "--oneline"}}, ""},
		{"git_read a write command", "git_read", map[string]any{"command": "commit", "args": []string{"-m", "x"}}, "G2"},
		{"git_read push", "git_read", map[string]any{"command": "push"}, "G2"},
		{"git_read log output flag", "git_read", map[string]any{"command": "log", "args": []string{"--output=x"}}, "G2"},
		{"git_read config override", "git_read", map[string]any{"command": "log", "args": []string{"-c", "core.pager=sh"}}, "G2"},
		{"git_read show a secret blob", "git_read", map[string]any{"command": "show", "args": []string{"HEAD:.env"}}, "G4"},
		{"git_read a path outside", "git_read", map[string]any{"command": "log", "args": []string{"--", "../outside"}}, "G2"},
		{"submit_finding", "submit_finding", map[string]any{"rule": "r", "file": "internal/lease/clock.go", "line": 1, "quote": "q", "note": "n"}, ""},
		{"submit_finding outside", "submit_finding", map[string]any{"rule": "r", "file": "../x.go", "line": 1, "quote": "q", "note": "n"}, "G2"},
		{"run test", "run", map[string]any{"class": "test", "args": []string{}}, ""},
		{"run format", "run", map[string]any{"class": "format", "args": []string{}}, "R1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newTestGuard(t, f, "reviewer", reviewerPerms(), nil, 100)
			checkVerdict(t, g.Check(Call{Tool: c.tool, Args: jsonArgs(t, c.args)}), c.row)
		})
	}
	t.Run("planner lint_backlog", func(t *testing.T) {
		p := Permissions{Tools: []string{"read", "search", "lint_backlog"}}
		g := newTestGuard(t, f, "planner", p, nil, 100)
		checkVerdict(t, g.Check(Call{Tool: "lint_backlog", Args: jsonArgs(t, map[string]any{"draft": "x"})}), "")
		checkVerdict(t, g.Check(Call{Tool: "run", Args: jsonArgs(t, map[string]any{"class": "test"})}), "G1")
	})
}

func TestGuardFailsClosed(t *testing.T) {
	f := newFixture(t)
	t.Run("empty permissions refuse every tool", func(t *testing.T) {
		g := newTestGuard(t, f, "builder", Permissions{}, nil, 100)
		for _, tool := range []string{"read", "search", "edit", "write", "run", "diff", "git_read", "skill", "submit_finding", "task_done", "lint_backlog", "x.y", ""} {
			checkVerdict(t, g.Check(Call{Tool: tool, Args: json.RawMessage(`{}`)}), "G1")
		}
	})
	t.Run("malformed input is refused", func(t *testing.T) {
		g := newTestGuard(t, f, "builder", builderPerms(), []string{"**"}, 100)
		for _, raw := range []string{`{`, `[]`, `"x"`, `{"path": 5}`, `{"path": "a"} {"path": "b"}`, `nul`, `{"path": "a\u0000b"}`} {
			v := g.Check(Call{Tool: "read", Args: json.RawMessage(raw)})
			if v.Allowed {
				t.Errorf("args %s were allowed", raw)
			}
		}
		if v := g.Check(Call{Tool: "read"}); v.Allowed {
			t.Error("a read with no arguments was allowed")
		}
	})
	t.Run("a files list of nothing refuses every builder write", func(t *testing.T) {
		g := newTestGuard(t, f, "builder", builderPerms(), nil, 100)
		checkVerdict(t, g.Check(Call{Tool: "write", Args: jsonArgs(t, map[string]any{"path": "internal/lease/a.go", "content": "x"})}), "B1")
	})
	t.Run("a registry guard list cannot drop the floor", func(t *testing.T) {
		p := builderPerms()
		p.Guard = nil
		g := newTestGuard(t, f, "builder", p, []string{"**"}, 100)
		checkVerdict(t, g.Check(Call{Tool: "edit", Args: jsonArgs(t, map[string]any{"path": ".git", "old": "a", "new": "b"})}), "G3")
		checkVerdict(t, g.Check(Call{Tool: "read", Args: jsonArgs(t, map[string]any{"path": ".env"})}), "G4")
		checkVerdict(t, g.Check(Call{Tool: "run", Args: jsonArgs(t, map[string]any{"class": "deploy"})}), "R1")
	})
	t.Run("missing floor row fails to compile", func(t *testing.T) {
		rows, _ := LoadRows(f.Home)
		var short []Row
		for _, r := range rows {
			if r.ID != "G3" {
				short = append(short, r)
			}
		}
		if _, err := NewGuard(short, Binding{Agent: "builder", Permissions: builderPerms(), Worktree: f.Root}, 3); err == nil {
			t.Fatal("rows without G3 compiled")
		}
	})
	t.Run("unknown guard row in permissions fails to compile", func(t *testing.T) {
		rows, _ := LoadRows(f.Home)
		p := builderPerms()
		p.Guard = append(p.Guard, "X9")
		if _, err := NewGuard(rows, Binding{Agent: "builder", Permissions: p, Worktree: f.Root}, 3); err == nil {
			t.Fatal("unknown row X9 compiled")
		}
	})
	t.Run("a binding without a worktree or limit fails to compile", func(t *testing.T) {
		rows, _ := LoadRows(f.Home)
		if _, err := NewGuard(rows, Binding{Agent: "builder", Permissions: builderPerms()}, 3); err == nil {
			t.Error("no worktree compiled")
		}
		if _, err := NewGuard(rows, Binding{Agent: "builder", Permissions: builderPerms(), Worktree: f.Root}, 0); err == nil {
			t.Error("refusal limit 0 compiled")
		}
	})
}

func TestGuardOutsideServers(t *testing.T) {
	f := newFixture(t)
	args := json.RawMessage(`{"q":"x"}`)
	p := Permissions{Tools: []string{"read", "docs.search"}, Servers: []string{"docs"}}
	g := newTestGuard(t, f, "reviewer", p, nil, 100)
	checkVerdict(t, g.Check(Call{Tool: "docs.search", Args: args}), "")
	checkVerdict(t, g.Check(Call{Tool: "docs.other", Args: args}), "G1")
	checkVerdict(t, g.Check(Call{Tool: "docs.search", Args: json.RawMessage(`[1]`)}), "G1")
	p.Servers = nil
	g = newTestGuard(t, f, "reviewer", p, nil, 100)
	checkVerdict(t, g.Check(Call{Tool: "docs.search", Args: args}), "G1")
}

func TestGuardRefusalLimit(t *testing.T) {
	f := newFixture(t)
	g := newTestGuard(t, f, "builder", builderPerms(), []string{"**"}, 3)
	bad := Call{Tool: "read", Args: jsonArgs(t, map[string]any{"path": "../outside/secret.txt"})}
	secret := Call{Tool: "read", Args: jsonArgs(t, map[string]any{"path": ".env"})}
	for i := 1; i <= 2; i++ {
		if v := g.Check(bad); v.Allowed || v.EndSession || v.Count != i {
			t.Fatalf("G2 refusal %d: %+v", i, v)
		}
	}
	if v := g.Check(secret); v.EndSession || v.Row != "G4" || v.Count != 1 {
		t.Fatalf("a refusal under another row counts apart: %+v", v)
	}
	v := g.Check(bad)
	if !v.EndSession || v.Row != "G2" || !strings.Contains(v.Message, "G6") {
		t.Fatalf("the third G2 refusal must end the session and name G6: %+v", v)
	}
	if v := g.Check(Call{Tool: "read", Args: jsonArgs(t, map[string]any{"path": "README.md"})}); v.Allowed || !v.EndSession {
		t.Fatalf("a call after the session ended must be refused: %+v", v)
	}
	if n := g.Refusals()["G2"]; n != 3 {
		t.Fatalf("Refusals G2 = %d, want 3", n)
	}
}

func TestGuardSetFilesAfterScopeAnswer(t *testing.T) {
	f := newFixture(t)
	g := newTestGuard(t, f, "builder", builderPerms(), []string{"internal/lease/**"}, 100)
	w := Call{Tool: "write", Args: jsonArgs(t, map[string]any{"path": "cmd/extra.go", "content": "package main\n"})}
	checkVerdict(t, g.Check(w), "B1")
	g.SetFiles([]string{"internal/lease/**", "cmd/extra.go"})
	checkVerdict(t, g.Check(w), "")
}

func TestGuardSiblingRules(t *testing.T) {
	f := newFixture(t)
	g := newTestGuard(t, f, "builder", builderPerms(), []string{"internal/lease/clock.go", "web/app.ts", "py/mod.py", "crates/foo/src/**"}, 100)
	write := func(p string) Verdict {
		return g.Check(Call{Tool: "write", Args: jsonArgs(t, map[string]any{"path": p, "content": "x"})})
	}
	for p, want := range map[string]string{
		"internal/lease/clock_test.go": "",
		"internal/lease/other_test.go": "B1",
		"web/app.test.ts":              "",
		"web/app.spec.ts":              "",
		"web/other.test.ts":            "B1",
		"py/test_mod.py":               "",
		"py/mod_test.py":               "",
		"py/test_other.py":             "B1",
		"crates/foo/tests/it.rs":       "",
		"crates/bar/tests/it.rs":       "B1",
	} {
		checkVerdict(t, write(p), want)
	}
}

func TestGuardHomePatternsStayOutOfTheWorktree(t *testing.T) {
	f := newFixture(t)
	rows, _ := LoadRows(f.Home)
	var g3 Row
	for _, r := range rows {
		if r.ID == "G3" {
			g3 = r
		}
	}
	if !strings.Contains(strings.Join(g3.Pattern, " "), filepath.ToSlash(f.Home)+"/.komodo/**") {
		t.Fatalf("home pattern not expanded: %v", g3.Pattern)
	}
	// A worktree under ~/.komodo/wt is not protected by the home-anchored pattern.
	wt := filepath.Join(f.Home, ".komodo", "wt", "shop", "w1")
	if err := os.MkdirAll(filepath.Join(wt, "internal", "lease"), 0o755); err != nil {
		t.Fatal(err)
	}
	g, err := NewGuard(rows, Binding{Agent: "builder", Permissions: builderPerms(), Worktree: wt, Files: []string{"**"}}, 3)
	if err != nil {
		t.Fatal(err)
	}
	checkVerdict(t, g.Check(Call{Tool: "write", Args: jsonArgs(t, map[string]any{"path": "internal/lease/a.go", "content": "x"})}), "")
}

func TestGuardScopeDenied(t *testing.T) {
	f := newFixture(t)
	g := newTestGuard(t, f, "builder", builderPerms(), []string{"internal/lease/**"}, 3)
	for p, want := range map[string]RowID{
		"~/.komodo/x":     "G2",
		"/etc/passwd":     "G2",
		"../outside/x.go": "G2",
		".git/config":     "G3",
		"docs/prd.md":     "G3",
		".env":            "G4",
		"certs.pem":       "G4",
	} {
		if row, ok := g.ScopeDenied(p); !ok || row != want {
			t.Errorf("ScopeDenied(%q) = %q, %v; want %s", p, row, ok, want)
		}
	}
	if row, ok := g.ScopeDenied("cmd/extra.go"); ok {
		t.Errorf("an ordinary path was denied by %s", row)
	}
}

func TestPolicyRowsMatchTheTable(t *testing.T) {
	rows, err := LoadRows("")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(rows)
	sum := sha256.Sum256(b)
	if got := hex.EncodeToString(sum[:]); got != rowsDigest {
		t.Fatalf("policy.json rows changed (digest %s): add or update a guard case, then pin the new digest", got)
	}
	covered := map[RowID]bool{"G1": true, "G2": true, "G3": true, "G4": true, "G5": true, "G6": true, "R1": true, "B1": true}
	for _, r := range rows {
		if !covered[r.ID] {
			t.Errorf("row %s has no guard case", r.ID)
		}
	}
	known := KnownRows()
	for id := range covered {
		if !known[string(id)] {
			t.Errorf("KnownRows lacks %s", id)
		}
	}
}
