//go:build !windows

package integration

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
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

// Agent roles: the agent registry holds the 4 roles planner, builder, tester and reviewer.
func TestAgentRegistryHoldsFourRoles(t *testing.T) {
	var got []string
	for _, r := range agent.Roles() {
		got = append(got, r.Name)
	}
	slices.Sort(got)
	if want := []string{"builder", "planner", "reviewer", "tester"}; !slices.Equal(got, want) {
		t.Fatalf("roles = %v, want %v", got, want)
	}
}

// Storage: a half-written lease record is never read, a leftover lock file expires instead of blocking, and go.mod lists no
// modules.
func TestStorageHalfRecordNeverReadAndStaleLockExpires(t *testing.T) {
	home := t.TempDir()
	ls, err := lease.New(home, 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	req := lease.GrantRequest{
		Repo: repo, Group: group, Run: run, Branch: "komodo/ep-1-tg-1.2", Worktree: "/wt/tg-1.2", ClonePath: "/src/shop",
	}
	if _, err := ls.Grant(ctx, req); err != nil {
		t.Fatal(err)
	}
	var record, lock string
	_ = filepath.WalkDir(filepath.Join(home, "leases"), func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil || !d.Type().IsRegular():
		case strings.HasSuffix(p, ".lock"):
			lock = p
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
	if lock == "" {
		lock = record + ".lock"
	}
	if err := os.WriteFile(lock, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(lock, old, old)
	done := make(chan error, 1)
	go func() {
		_, err := ls.Grant(ctx, lease.GrantRequest{
			Repo: repo, Group: "0198f3a2-7a10-7d3e-9b2f-6c1d8e4a5f31", Run: run, Branch: "komodo/ep-1-tg-1.3",
			Worktree: "/wt/tg-1.3", ClonePath: "/src/shop",
		})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a stale lock file blocked a grant: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a stale lock file blocked a grant for 10 seconds")
	}
	mod, err := os.ReadFile(filepath.Join(mustWd(), "..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mod), "require") {
		t.Fatalf("go.mod lists modules:\n%s", mod)
	}
}

// Machine-wide records: only the State Manager (internal/state) writes providers.json, update.json, install.json and
// schedule.json.
func TestOnlyTheStateManagerWritesMachineRecords(t *testing.T) {
	root := filepath.Join(mustWd(), "..", "..")
	writers := []string{"WriteFile(", "os.Create(", "os.Rename(", "OpenFile(", "Atomic", "WriteJSON(", "Save("}
	var bad []string
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "testdata" || d.Name() == "state") {
			return fs.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		for i, line := range strings.Split(string(b), "\n") {
			for _, rec := range []string{"providers.json", "update.json", "install.json", "schedule.json"} {
				if !strings.Contains(line, `"`+rec+`"`) || strings.HasPrefix(strings.TrimSpace(line), "//") {
					continue
				}
				for _, w := range writers {
					if strings.Contains(line, w) {
						bad = append(bad, fmt.Sprintf("%s:%d writes %s with %s", rel, i+1, rec, w))
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Fatalf("code outside internal/state that may write a machine record:\n%s", strings.Join(bad, "\n"))
	}
}
