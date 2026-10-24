//go:build !windows

package integration

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/lease"
)

// Command list: `komodo --list` names the 13 commands and build, test and review exit 2 as unknown commands.
func TestCommandListNamesThirteenAndOthersAreUnknown(t *testing.T) {
	r := newBinRig(t, nil)
	res := r.komodo(30*time.Second, "--list")
	if res.code != 0 {
		t.Fatalf("komodo --list exited %d\n%s\n%s", res.code, res.stdout, res.stderr)
	}
	section, _, _ := strings.Cut(res.stdout, "Global flags:")
	_, section, _ = strings.Cut(section, "Commands:")
	names := map[string]bool{}
	for _, line := range strings.Split(section, "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			names[f[0]] = true
		}
	}
	want := []string{
		"install", "config", "plan", "run", "stop", "replay", "status", "abandon", "ui", "doctor", "update", "login", "clean",
	}
	if len(names) != 13 {
		t.Fatalf("the list names %d commands, want 13: %v", len(names), names)
	}
	for _, n := range want {
		if !names[n] {
			t.Errorf("the list lacks %q", n)
		}
	}
	for _, c := range []string{"build", "test", "review"} {
		if res := r.komodo(30*time.Second, c); res.code != 2 || !strings.Contains(res.stdout, "unknown command") {
			t.Errorf("komodo %s exited %d, want 2 with an unknown command message\n%s", c, res.code, res.stdout)
		}
	}
}

// Agent roles: the agent registry holds the 4 roles planner, builder, Quality Assurance and reviewer.
func TestAgentRegistryHoldsFourRoles(t *testing.T) {
	var got []string
	for _, r := range agent.Roles() {
		got = append(got, r.Name)
	}
	slices.Sort(got)
	if want := []string{"builder", "planner", "qa", "reviewer"}; !slices.Equal(got, want) {
		t.Fatalf("roles = %v, want %v", got, want)
	}
}

// Storage: a half-written lease record is never read, the locks of a killed process free instead of blocking, and go.mod
// lists no modules.
func TestStorageHalfRecordNeverReadAndStaleLockExpires(t *testing.T) {
	home := t.TempDir()
	ls, err := lease.New(home, 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	req := lease.GrantRequest{
		Repo: repo, Group: group, Run: run, Worktree: "/wt/tg-1.2", ClonePath: "/src/shop",
	}
	if _, err := ls.Grant(ctx, req); err != nil {
		t.Fatal(err)
	}
	var record string
	_ = filepath.WalkDir(filepath.Join(home, "leases"), func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil || !d.Type().IsRegular():
		case strings.HasSuffix(p, ".lock"):
		case filepath.Base(p) != ".fences.json" && strings.Contains(p, group):
			record = p
		}
		return nil
	})
	if record == "" {
		t.Fatal("the grant wrote no lease record")
	}
	b, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record, b[:len(b)/2], 0o644); err != nil {
		t.Fatal(err)
	}
	if l, ok, err := ls.Holder(ctx, repo, group); err == nil && ok {
		t.Fatalf("a half record was read as lease %+v", l)
	}
	// A kill mid-write leaves a partial temporary file beside the intact record, never a half record.
	if err := os.WriteFile(record, b, 0o644); err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(filepath.Dir(record), ".tmp-partial")
	if err := os.WriteFile(partial, b[:len(b)/3], 0o644); err != nil {
		t.Fatal(err)
	}
	if l, ok, err := ls.Holder(ctx, repo, group); err != nil || !ok || l.Run != run {
		t.Fatalf("the intact record was not read beside a partial file: %+v %v %v", l, ok, err)
	}
	// A process that holds the repository's grant lock and the second group's lock is killed mid-hold, as a crash would.
	second := "0198f3a2-7a10-7d3e-9b2f-6c1d8e4a5f31"
	dir := filepath.Dir(record)
	held := []string{filepath.Join(dir, ".repo.lock"), filepath.Join(dir, second+lease.LockSuffix)}
	child := exec.Command(os.Args[0], "-test.run=^TestLockHolderProcess$")
	child.Env = append(os.Environ(), lockHoldEnv+"="+strings.Join(held, string(os.PathListSeparator)))
	out, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	sc := bufio.NewScanner(out)
	for locked := false; !locked; {
		if !sc.Scan() {
			t.Fatalf("the child never reported holding the locks: %v", sc.Err())
		}
		locked = sc.Text() == "locked"
	}
	next := lease.GrantRequest{
		Repo: repo, Group: second, Run: run, Worktree: "/wt/tg-1.3", ClonePath: "/src/shop",
	}
	if !core.LockHeld(held[0]) || !core.LockHeld(held[1]) {
		t.Fatal("the child does not hold the lock files")
	}
	if _, err := ls.Grant(ctx, next); !errors.Is(err, lease.ErrLocked) {
		t.Fatalf("a grant while another process held the locks returned %v, want ErrLocked", err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	done := make(chan error, 1)
	go func() {
		_, err := ls.Grant(ctx, next)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the locks of a killed process blocked a grant: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the locks of a killed process blocked a grant for 10 seconds")
	}
	mod, err := os.ReadFile(filepath.Join(mustWd(), "..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mod), "require") {
		t.Fatalf("go.mod lists modules:\n%s", mod)
	}
}

// lockHoldEnv names the lock files, joined by the path list separator, that TestLockHolderProcess holds.
const lockHoldEnv = "KOMODO_TEST_HOLD_LOCKS"

// TestLockHolderProcess is the child of the storage check: it takes the locks named in the environment, reports it holds
// them and waits to be killed. Without the environment variable it does nothing.
func TestLockHolderProcess(t *testing.T) {
	paths := os.Getenv(lockHoldEnv)
	if paths == "" {
		return
	}
	for _, p := range strings.Split(paths, string(os.PathListSeparator)) {
		if _, err := core.FileLock(context.Background(), p, 5*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	fmt.Println("locked")
	time.Sleep(time.Minute)
}

// machineRecords are the files only the State Manager writes.
var machineRecords = []string{"providers.json", "update.json", "install.json", "schedule.json"}

// Machine-wide records: only the State Manager (internal/state) writes providers.json, update.json, install.json and
// schedule.json. The scan covers internal/ and cmd/, and flags a function that names a record path (a literal, or a constant
// or variable declared from one) and calls a writer anywhere in its body.
func TestOnlyTheStateManagerWritesMachineRecords(t *testing.T) {
	root := filepath.Join(mustWd(), "..", "..")
	var bad []string
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && (d.Name() == "testdata" || p == filepath.Join(root, "internal", "state")) {
				return fs.SkipDir
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			found, err := recordWriters(p)
			for _, f := range found {
				bad = append(bad, rel+": "+f)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(bad) > 0 {
		t.Fatalf("code outside internal/state that may write a machine record:\n%s", strings.Join(bad, "\n"))
	}
}

// recordWriters parses one file and describes each function that names a machine record and calls a writer.
func recordWriters(path string) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return nil, err
	}
	// holders are the package-level names declared from a record path.
	holders := map[string]string{}
	for _, decl := range file.Decls {
		if gd, ok := decl.(*ast.GenDecl); ok && gd.Tok != token.IMPORT && gd.Tok != token.TYPE {
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for _, rec := range recordsIn(vs) {
					for _, n := range vs.Names {
						holders[n.Name] = rec
					}
				}
			}
		}
	}
	var out []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		recs, writes := map[string]bool{}, map[string]bool{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.BasicLit:
				for _, rec := range recordsIn(v) {
					recs[rec] = true
				}
			case *ast.Ident:
				if rec, ok := holders[v.Name]; ok {
					recs[rec] = true
				}
			case *ast.CallExpr:
				if name := calleeName(v); isWriter(name) {
					writes[name] = true
				}
			}
			return true
		})
		if len(recs) > 0 && len(writes) > 0 {
			out = append(out, fmt.Sprintf("%s names %v and calls %v", fn.Name.Name, slices.Sorted(maps.Keys(recs)),
				slices.Sorted(maps.Keys(writes))))
		}
	}
	return out, nil
}

// recordsIn returns the machine records named by the path-like string literals under a node; a literal with a space is a
// message, not a path.
func recordsIn(n ast.Node) []string {
	var out []string
	ast.Inspect(n, func(c ast.Node) bool {
		if lit, ok := c.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			v, err := strconv.Unquote(lit.Value)
			if err != nil || strings.Contains(v, " ") {
				return true
			}
			for _, rec := range machineRecords {
				if v == rec || strings.HasSuffix(v, "/"+rec) {
					out = append(out, rec)
				}
			}
		}
		return true
	})
	return out
}

// calleeName returns the name of the function a call invokes.
func calleeName(c *ast.CallExpr) string {
	switch f := c.Fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// isWriter reports whether a function name writes a file.
func isWriter(name string) bool {
	return slices.Contains([]string{"WriteFile", "Create", "Rename", "OpenFile", "WriteJSON", "Save"}, name) ||
		strings.Contains(name, "Atomic")
}
