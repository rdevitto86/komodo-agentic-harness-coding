package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBackend records every call and fails on request.
type fakeBackend struct {
	mu         sync.Mutex
	name       string
	checkErr   error
	prepareErr error
	commandErr error
	releaseErr error
	prepared   []Spec
	commanded  []Spec
	released   []string
	cut        []string
	caveats    []string
	next       int
}

// Name returns the fake's name.
func (f *fakeBackend) Name() string { return f.name }

// Check returns the configured error.
func (f *fakeBackend) Check() error { return f.checkErr }

// Prepare records the spec and returns a numbered instance.
func (f *fakeBackend) Prepare(_ context.Context, s Spec) (Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.prepareErr != nil {
		return Instance{}, f.prepareErr
	}
	f.prepared = append(f.prepared, s)
	f.next++
	return Instance{ID: f.name + "-" + string(rune('a'+f.next)), Group: s.Group, Backend: f.name, CreatedAt: time.Now()}, nil
}

// Command records the spec it was given and returns a harmless command.
func (f *fakeBackend) Command(_ context.Context, _ Instance, s Spec, c Command) (*exec.Cmd, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.commandErr != nil {
		return nil, f.commandErr
	}
	f.commanded = append(f.commanded, s)
	return exec.Command(c.Argv[0], c.Argv[1:]...), nil
}

// Release records the released id.
func (f *fakeBackend) Release(_ context.Context, i Instance) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, i.ID)
	return f.releaseErr
}

// CutNetwork records the instance whose network was cut.
func (f *fakeBackend) CutNetwork(_ context.Context, i Instance) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cut = append(f.cut, i.ID)
	return nil
}

// Caveats returns the configured caveats.
func (f *fakeBackend) Caveats(Spec) []string { return f.caveats }

// plainOnly hides the optional interfaces of the fake it wraps.
type plainOnly struct{ f *fakeBackend }

func (p plainOnly) Name() string { return p.f.Name() }
func (p plainOnly) Check() error { return p.f.Check() }
func (p plainOnly) Prepare(ctx context.Context, s Spec) (Instance, error) {
	return p.f.Prepare(ctx, s)
}
func (p plainOnly) Command(ctx context.Context, i Instance, s Spec, c Command) (*exec.Cmd, error) {
	return p.f.Command(ctx, i, s, c)
}
func (p plainOnly) Release(ctx context.Context, i Instance) error { return p.f.Release(ctx, i) }

// realPath returns the symlink-free form of a temporary folder.
func realPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// testSpec builds a Spec over real folders in a temporary tree.
func testSpec(t *testing.T, role string) Spec {
	t.Helper()
	root := realPath(t, t.TempDir())
	dirs := map[string]string{
		"wt": filepath.Join(root, "wt"), "git": filepath.Join(root, "repo", ".git"),
		"tmp": filepath.Join(root, "tmp"), "build": filepath.Join(root, "lang", "build"),
		"mod": filepath.Join(root, "lang", "modules"), "tool": filepath.Join(root, "toolchain"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dirs["wt"], ".git"), []byte("gitdir: "+dirs["git"]+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return Spec{
		Group: "g1", Worktree: dirs["wt"], GitDir: dirs["git"],
		Writable: []string{dirs["tmp"], dirs["build"]}, Modules: dirs["mod"],
		Toolchains: []string{dirs["tool"]}, MaxProcs: 512, MaxMemGB: 8, Role: role,
	}
}

// fakeManager returns a Manager over a builder fake, a tester fake and a fallback fake.
func fakeManager() (*Manager, *fakeBackend, *fakeBackend, *fakeBackend) {
	b := &fakeBackend{name: "seatbelt"}
	d := &fakeBackend{name: "docker"}
	fb := &fakeBackend{name: "seatbelt-fallback"}
	return NewManager(map[string]Backend{RoleBuilder: b, RoleTester: d}, fb), b, d, fb
}

// putFake writes an executable shell script named name into a new folder and puts the folder first on PATH.
func putFake(t *testing.T, name, script string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// emptyPath makes PATH an empty folder so no program is found.
func emptyPath(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

// contains reports whether the list holds the item.
func contains(list []string, item string) bool {
	for _, s := range list {
		if s == item {
			return true
		}
	}
	return false
}

// indexOf returns the position of the first run of items in the list, or -1.
func indexOf(list []string, seq ...string) int {
	for i := 0; i+len(seq) <= len(list); i++ {
		if strings.Join(list[i:i+len(seq)], "\x00") == strings.Join(seq, "\x00") {
			return i
		}
	}
	return -1
}

var errBoom = errors.New("boom")
