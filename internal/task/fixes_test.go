package task

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fix 1: git_read classifies the object path of every revision argument the way git reads it.
func TestGitReadObjectPaths(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		name string
		args []string
		row  string
	}{
		{"index path", []string{":.env"}, "G4"},
		{"index stage zero", []string{":0:.env"}, "G4"},
		{"index stage two", []string{":2:sub/.env"}, "G4"},
		{"rev with braces and colon", []string{"HEAD^{/x:y}:.env"}, "G4"},
		{"rev path", []string{"HEAD:sub/.env"}, "G4"},
		{"rev path with dot slash", []string{"HEAD:./.env"}, "G4"},
		{"line range with a file", []string{"-L", "1,5:.env"}, "G4"},
		{"object path escapes", []string{"HEAD:../x"}, "G2"},
		{"index path escapes", []string{":../x"}, "G2"},
		{"unbalanced brace", []string{"HEAD^{/x:.env"}, "G2"},
		{"colon in an index path", []string{":foo:.env"}, "G2"},
		{"function range with a file", []string{":funcname:.env"}, "G2"},
		{"pathspec magic after --", []string{"--", ":(top).env"}, "G2"},
		{"plain object path", []string{"HEAD:README.md"}, ""},
		{"plain index path", []string{":README.md"}, ""},
		{"brace colon then plain path", []string{"HEAD^{/x:y}:README.md"}, ""},
		{"commit search", []string{":/fix"}, ""},
		{"stage plain", []string{":0:README.md"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newTestGuard(t, f, "reviewer", reviewerPerms(), nil, 100)
			v := g.Check(Call{Tool: "git_read", Args: jsonArgs(t, map[string]any{"command": "show", "args": c.args})})
			checkVerdict(t, v, c.row)
		})
	}
}

// Fix 2: the tools use the path the guard resolved and refuse a link swapped in afterwards.
func TestSwappedLinkIsRefused(t *testing.T) {
	t.Run("read", func(t *testing.T) {
		r := newRig(t, nil)
		writeFile(t, filepath.Join(r.f.Root, "swap.txt"), "inside\n")
		secret := filepath.Join(r.f.Outside, "secret.txt")
		r.env.afterResolve = func() {
			_ = os.Remove(filepath.Join(r.f.Root, "swap.txt"))
			_ = os.Symlink(secret, filepath.Join(r.f.Root, "swap.txt"))
		}
		got := r.call("read", map[string]any{"path": "swap.txt"})
		if strings.Contains(got.Text, "outside") {
			t.Fatalf("read followed a swapped link: %q", got.Text)
		}
	})
	t.Run("edit", func(t *testing.T) {
		r := newRig(t, nil)
		writeFile(t, filepath.Join(r.f.Root, "ed.txt"), "inside\n")
		secret := filepath.Join(r.f.Outside, "secret.txt")
		r.env.afterResolve = func() {
			_ = os.Remove(filepath.Join(r.f.Root, "ed.txt"))
			_ = os.Symlink(secret, filepath.Join(r.f.Root, "ed.txt"))
		}
		got := r.call("edit", map[string]any{"path": "ed.txt", "old": "outside", "new": "changed"})
		if !strings.HasPrefix(got.Text, "error:") {
			t.Fatalf("edit followed a swapped link: %q", got.Text)
		}
		if b, _ := os.ReadFile(secret); string(b) != "outside\n" {
			t.Fatalf("the outside file changed: %q", b)
		}
	})
	t.Run("write", func(t *testing.T) {
		r := newRig(t, nil)
		r.env.afterResolve = func() { _ = os.Symlink(r.f.Outside, filepath.Join(r.f.Root, "lnk2")) }
		got := r.call("write", map[string]any{"path": "lnk2/new.txt", "content": "x"})
		if _, err := os.Stat(filepath.Join(r.f.Outside, "new.txt")); err == nil {
			t.Fatalf("write created a file outside the worktree: %q", got.Text)
		}
	})
	t.Run("search", func(t *testing.T) {
		r := newRig(t, nil)
		writeFile(t, filepath.Join(r.f.Root, "d/a.txt"), "needle\n")
		writeFile(t, filepath.Join(r.f.Outside, "o.txt"), "needle outside\n")
		r.env.afterResolve = func() {
			_ = os.RemoveAll(filepath.Join(r.f.Root, "d"))
			_ = os.Symlink(r.f.Outside, filepath.Join(r.f.Root, "d"))
		}
		got := r.call("search", map[string]any{"pattern": "needle", "path": "d"})
		if strings.Contains(got.Text, "outside") {
			t.Fatalf("search followed a swapped link: %q", got.Text)
		}
	})
}

// Fix 3: secret files stay hidden in combined diffs, quoted headers and headers that cannot be read.
func TestHideSecretDiffsFailsClosed(t *testing.T) {
	f := newFixture(t)
	g := newTestGuard(t, f, "reviewer", reviewerPerms(), nil, 100)
	cases := map[string]string{
		"combined":          "diff --cc .env\nindex 1,2..3\n--- a/.env\n+++ b/.env\n@@@ -1 -1 +1 @@@\n  TOKEN=hunter2\n",
		"combined long":     "diff --combined sub/.env\n@@@ -1 -1 +1 @@@\n++TOKEN=hunter2\n",
		"quoted":            "diff --git \"a/.env\" \"b/.env\"\n+TOKEN=hunter2\n",
		"quoted octal":      "diff --git \"a/sec\\303\\251/.env\" \"b/sec\\303\\251/.env\"\n+TOKEN=hunter2\n",
		"quoted tab":        "diff --git \"a/x\\t.pem\" \"b/x\\t.pem\"\n+TOKEN=hunter2\n",
		"unterminated":      "diff --git \"a/.env b/.env\n+TOKEN=hunter2\n",
		"unknown form":      "diff --weird .env\n+TOKEN=hunter2\n",
		"rename to secret":  "diff --git a/x.go b/.env\nrename from x.go\nrename to .env\n+TOKEN=hunter2\n",
		"name with a space": "diff --git a/my dir/.env b/my dir/.env\n+TOKEN=hunter2\n",
	}
	for name, patch := range cases {
		t.Run(name, func(t *testing.T) {
			out := hideSecretDiffs(patch, g)
			if strings.Contains(out, "hunter2") {
				t.Fatalf("secret body shown:\n%s", out)
			}
		})
	}
	t.Run("log keeps the commit lines after a hidden section", func(t *testing.T) {
		patch := "commit aaa\n\n    first\n\ndiff --git a/.env b/.env\n+TOKEN=hunter2\ncommit bbb\n\n    second " +
			"message\n\ndiff --git a/a.go b/a.go\n+func A() {}\n"
		out := hideSecretDiffs(patch, g)
		if strings.Contains(out, "hunter2") || !strings.Contains(out, "second message") ||
			!strings.Contains(out, "+func A() {}") || !strings.Contains(out, "commit bbb") {
			t.Fatalf("out:\n%s", out)
		}
	})
}

// Fix 4: a tests folder belongs to a Rust crate with a Cargo.toml and holds Rust files only.
func TestTestsFolderIsRustOnly(t *testing.T) {
	f := newFixture(t)
	for _, p := range []string{"crates/foo/Cargo.toml", "crates/foo/vendor/bar/Cargo.toml"} {
		writeFile(t, filepath.Join(f.Root, p), "[package]\n")
	}
	files := []string{"py/mod.py", "crates/foo/src/**", "web/app.ts"}
	g := newTestGuard(t, f, "builder", builderPerms(), files, 100)
	write := func(p string) Verdict {
		return g.Check(Call{Tool: "write", Args: jsonArgs(t, map[string]any{"path": p, "content": "x"})})
	}
	for p, want := range map[string]string{
		"py/tests/evil.py":                     "B1",
		"web/tests/evil.ts":                    "B1",
		"tests/evil.rs":                        "B1",
		"crates/foo/tests/it.rs":               "",
		"crates/foo/tests/data.json":           "B1",
		"crates/foo/vendor/bar/tests/it.rs":    "B1",
		"crates/foo/tests/nested/deeper/it.rs": "",
		"crates/nocargo/tests/it.rs":           "B1",
	} {
		checkVerdict(t, write(p), want)
	}
}

// Fix 6: a git error names the subcommand, never the host folders.
func TestHostGitErrorsHideHostPaths(t *testing.T) {
	r := newGitRepo(t)
	bogus := filepath.Join(t.TempDir(), "no-such-git-dir")
	l := r.Lease
	l.GitDir = bogus
	ptr, err := RecordPointer(l.Worktree)
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHostGit(l, "", ptr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	_, err = h.ReadOnly(context.Background(), "status", nil)
	if err == nil {
		t.Fatal("git status on a missing git folder succeeded")
	}
	for _, leak := range []string{bogus, l.Worktree, "--git-dir", "--work-tree", h.empty} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("error %q leaks %q", err, leak)
		}
	}
	if !strings.Contains(err.Error(), "status") {
		t.Fatalf("error %q does not name the subcommand", err)
	}
}

// Fix 7: an outside server's listed variables are not passed, and the start says so.
func TestOutsideServerWarnsOfDroppedEnv(t *testing.T) {
	s := fakeServer()
	s.Env = []string{"TOKEN", "OTHER_KEY"}
	o := startFake(t, &fakeConfiner{dir: t.TempDir()}, s)
	w := strings.Join(o.Warnings(), "\n")
	for _, name := range s.Env {
		if !strings.Contains(w, name) {
			t.Fatalf("warnings do not name %s:\n%s", name, w)
		}
	}
}

// Fix 8: the tool server starts the outside servers its permissions name and binds their tools.
func TestToolServerBindStartsOutsideServers(t *testing.T) {
	f := newFixture(t)
	conf := &fakeConfiner{dir: t.TempDir(), env: []string{"KOMODO_FAKE_MCP=1"}}
	cfg := ToolServerConfig{Agent: "reviewer", Session: "s", Worktree: f.Root, Dir: t.TempDir(), RefusalLimit: 3,
		Permissions: Permissions{Tools: []string{"read", "fake.echo"}, Servers: []string{"fake"}},
		Servers:     []OutsideServer{fakeServer()}}
	b, err := cfg.Bind(f.Home, Env{Confiner: conf, Instance: "i1"})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	defer func() { _ = b.Close() }()
	if got := b.Dispatch(context.Background(), "fake.echo", json.RawMessage(`{"q":"x"}`)); got.Text != "echo:x" {
		t.Fatalf("outside call = %q", got.Text)
	}
}

// Fix 9: a file whose name starts with a dash reaches the format command as a path.
func TestFormatPathIsNeverAnOption(t *testing.T) {
	r := newRig(t, func(e *Env) { e.Format = []string{"sh", "-c", "echo \"$1\" > fmt.marker", "fmt"} })
	writeFile(t, filepath.Join(r.f.Root, "-x.go"), "a\n")
	r.call("edit", map[string]any{"path": "-x.go", "old": "a", "new": "b"})
	if b, _ := os.ReadFile(filepath.Join(r.f.Root, "fmt.marker")); strings.TrimSpace(string(b)) != "./-x.go" {
		t.Fatalf("format command saw %q", b)
	}
}

// Fix 10: host reads run no textconv or external diff driver named by a planted attributes file.
func TestHostGitReadsRunNoDiffDriver(t *testing.T) {
	r := newGitRepo(t)
	marker := filepath.Join(t.TempDir(), "ran")
	script := filepath.Join(t.TempDir(), "conv.sh")
	writeFile(t, script, "#!/bin/sh\ntouch '"+marker+"'\ncat \"$1\"\n")
	git(t, r.Main, "config", "diff.evil.textconv", script)
	git(t, r.Main, "config", "diff.external", script)
	writeFile(t, filepath.Join(r.Lease.Worktree, ".gitattributes"), "*.go diff=evil\n")
	git(t, r.Lease.Worktree, "show", "HEAD")
	if _, err := os.Stat(marker); err != nil {
		t.Skip("this git does not run the textconv driver on show, so the proof cannot be made here")
	}
	_ = os.Remove(marker)
	h := newHostGit(t, r)
	for _, c := range [][]string{{"show", "HEAD"}, {"log", "-p"}, {"blame", "a.go"}} {
		if _, err := h.ReadOnly(context.Background(), c[0], c[1:]); err != nil {
			t.Fatalf("git %s: %v", c[0], err)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a host read ran the planted diff driver")
	}
}
