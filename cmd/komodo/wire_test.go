package main

import (
	"context"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cache"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/fault"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/config"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/contextmgr"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/intake"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

// listTree is a tree of empty files with the given paths.
type listTree []string

// Hash returns a fixed fingerprint.
func (listTree) Hash() string { return "h" }

// Paths returns the paths.
func (t listTree) Paths() []string { return slices.Clone(t) }

// ReadFile returns no content.
func (listTree) ReadFile(string) ([]byte, error) { return nil, nil }

// Exists reports whether a path matches the glob.
func (t listTree) Exists(pattern string) bool {
	for _, p := range t {
		if ok, _ := path.Match(pattern, p); ok {
			return true
		}
	}
	return false
}

// The command classes and the language come from the files in the group's tree, not from a fixed Go table.
func TestCommandTableFollowsTheTreesMarkerFiles(t *testing.T) {
	cmds, lang := commandTable(contextmgr.Detect(listTree{"Cargo.toml", "src/lib.rs"}))
	if lang != "rust" {
		t.Fatalf("language %q, want rust", lang)
	}
	if got := cmds["test"]; !slices.Equal(got, []string{"cargo", "test"}) {
		t.Fatalf("test command %v, want cargo test", got)
	}
	if _, ok := cmds["git-read"]; ok {
		t.Fatal("git-read is not a command class of the table")
	}
	goCmds, goLang := commandTable(contextmgr.Detect(listTree{"go.mod", "a.go"}))
	if goLang != "go" || !slices.Equal(goCmds["build"], []string{"go", "build", "./..."}) {
		t.Fatalf("go table %v %q", goCmds, goLang)
	}
}

// A done_when command keeps its quoting: it runs through the shell, not through a split on spaces.
func TestDoneWhenRunsThroughTheShell(t *testing.T) {
	cfg, err := config.New(
		t.TempDir(),
		config.Known{GuardRows: task.KnownRows(), Tools: task.KnownTools()},
		func(string) bool { return true },
	)
	if err != nil {
		t.Fatal(err)
	}
	k := &repoKit{s: &stack{cfg: cfg}}
	g := intake.Group{Name: "tg-1.1", Tasks: []intake.Task{{Name: "tsk-a", DoneWhen: "go test -run 'A|B' ./..."}}}
	cmds := map[string][]string{}
	k.taskSpec(engine.GroupSpec{Label: "tg-1.1"}, g, cmds)
	want := []string{"sh", "-c", "go test -run 'A|B' ./..."}
	if got := cmds["task:tsk-a"]; !slices.Equal(got, want) {
		t.Fatalf("done_when command %q, want %q", got, want)
	}
}

// A cache the engine closed is closed for real, and the repository's handle keeps working by reopening it.
func TestCacheStoreClosesCachesAndHandlesSurviveAClose(t *testing.T) {
	store := &cacheStore{home: t.TempDir(), version: "t", open: map[string]*cache.Cache{}}
	first, err := store.Open(context.Background(), "repo")
	if err != nil {
		t.Fatal(err)
	}
	h := lazyCache{store, "repo"}
	key := h.Key("a")
	if err := h.Put(cache.NSSetup, key, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b, ok := h.Get(cache.NSSetup, h.Key("a")); !ok || string(b) != "x" {
		t.Fatalf("the handle lost its entry after a close: %q %v", b, ok)
	}
	if again, _ := store.Open(context.Background(), "repo"); again == first {
		t.Fatal("a closed cache was handed out again")
	}
}

// A kit that fails to open leaves no Context Manager goroutine behind.
func TestFailedKitStartsNoGoroutine(t *testing.T) {
	home := t.TempDir()
	notDir := filepath.Join(home, "file")
	if err := os.WriteFile(notDir, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	b := bus.New(bus.Options{})
	s := &stack{
		home:   home,
		bus:    b,
		ctx:    context.Background(),
		stderr: io.Discard,
		faults: fault.New(b, &engine.ConnPrinter{}),
		caches: &cacheStore{
			home:    home,
			version: "t",
			open:    map[string]*cache.Cache{},
		},
		kits: map[string]*repoKit{},
		byID: map[string]*repoKit{},
		refs: map[string]*groupRef{},
	}
	before := runtime.NumGoroutine()
	for range 5 {
		if _, err := s.kitFor(home, filepath.Join(notDir, "x"), "repo"); err == nil {
			t.Fatal("a kit opened over a state folder that cannot exist")
		}
	}
	time.Sleep(200 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+1 {
		t.Fatalf("goroutines %d before, %d after five failed kits", before, after)
	}
}

// The protected paths come from the guard policy's protected-path and secret-file rows.
func TestProtectedPathsComeFromTheGuardPolicy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got := protectedPaths()
	for _, want := range []string{"*.pem", "id_rsa*", ".git/**"} {
		if !slices.Contains(got, want) {
			t.Fatalf("protected paths %v lack %q", got, want)
		}
	}
}
