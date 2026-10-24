package sandbox

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestBuilderUsesTheOperatingSystemBackend(t *testing.T) {
	m, b, d, _ := fakeManager()
	inst, err := m.CreateSandboxInstance(context.Background(), testSpec(t, RoleBuilder))
	if err != nil {
		t.Fatal(err)
	}
	if inst.Backend != "seatbelt" || inst.Fallback || len(b.prepared) != 1 || len(d.prepared) != 0 {
		t.Fatalf("builder went to the wrong backend: %+v", inst)
	}
}

func TestPrepareSpecAlwaysHoldsTheGitPointerReadOnly(t *testing.T) {
	m, b, _, _ := fakeManager()
	s := testSpec(t, RoleBuilder)
	if _, err := m.CreateSandboxInstance(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(s.Worktree, ".git")
	if !contains(b.prepared[0].ReadOnly, want) {
		t.Fatalf("ReadOnly %v lacks %s", b.prepared[0].ReadOnly, want)
	}
	if len(s.ReadOnly) != 0 {
		t.Fatalf("the caller's Spec was changed: %v", s.ReadOnly)
	}
}

func TestQAUsesDockerWhenItWorks(t *testing.T) {
	m, _, d, fb := fakeManager()
	inst, err := m.CreateSandboxInstance(context.Background(), testSpec(t, RoleQA))
	if err != nil {
		t.Fatal(err)
	}
	if inst.Backend != "docker" || inst.Fallback || len(d.prepared) != 1 || len(fb.prepared) != 0 {
		t.Fatalf("Quality Assurance went to the wrong backend: %+v", inst)
	}
}

func TestQAFallsBackWhenDockerCheckFails(t *testing.T) {
	m, _, d, fb := fakeManager()
	d.checkErr = errors.New("the docker daemon does not answer: start Docker")
	inst, err := m.CreateSandboxInstance(context.Background(), testSpec(t, RoleQA))
	if err != nil {
		t.Fatal(err)
	}
	if !inst.Fallback || inst.Backend != "seatbelt-fallback" || !strings.Contains(inst.Reason, "start Docker") {
		t.Fatalf("fallback not recorded: %+v", inst)
	}
	if len(d.prepared) != 0 || len(fb.prepared) != 1 {
		t.Fatal("docker must not be prepared and the fallback must")
	}
}

func TestQADoesNotFallBackWhenDockerPrepareFails(t *testing.T) {
	m, _, d, fb := fakeManager()
	d.prepareErr = errBoom
	_, err := m.CreateSandboxInstance(context.Background(), testSpec(t, RoleQA))
	if !errors.Is(err, errBoom) || len(fb.prepared) != 0 {
		t.Fatalf("only a failed Check falls back: %v", err)
	}
}

func TestQAFailsWhenBothBackendsFail(t *testing.T) {
	m, _, d, fb := fakeManager()
	d.checkErr = errors.New("docker is not installed")
	fb.checkErr = errors.New("user namespaces are blocked")
	_, err := m.CreateSandboxInstance(context.Background(), testSpec(t, RoleQA))
	if err == nil || !strings.Contains(err.Error(), "user namespaces are blocked") ||
		!strings.Contains(err.Error(), "docker is not installed") {
		t.Fatalf("error must name both failures: %v", err)
	}
}

func TestBuilderCheckFailureNamesTheFixAndPreparesNothing(t *testing.T) {
	m, b, _, _ := fakeManager()
	b.checkErr = errors.New("sandbox-exec is missing: restore /usr/bin/sandbox-exec")
	_, err := m.CreateSandboxInstance(context.Background(), testSpec(t, RoleBuilder))
	if err == nil || !strings.Contains(err.Error(), "restore /usr/bin/sandbox-exec") || len(b.prepared) != 0 {
		t.Fatalf("got %v, prepared %d", err, len(b.prepared))
	}
}

func TestNoFallbackBackendMeansQAFailsOnDockerError(t *testing.T) {
	d := &fakeBackend{name: "docker", checkErr: errBoom}
	m := NewManager(map[string]Backend{RoleQA: d}, nil)
	if _, err := m.CreateSandboxInstance(context.Background(), testSpec(t, RoleQA)); err == nil {
		t.Fatal("want an error with no fallback")
	}
}

func TestInvalidSpecsAreRefused(t *testing.T) {
	m, b, _, _ := fakeManager()
	cases := map[string]func(*Spec){
		"no group":          func(s *Spec) { s.Group = "" },
		"unknown role":      func(s *Spec) { s.Role = "reviewer" },
		"relative worktree": func(s *Spec) { s.Worktree = "wt" },
		"empty worktree":    func(s *Spec) { s.Worktree = "" },
		"no temp folder":    func(s *Spec) { s.Writable = nil },
		"relative writable": func(s *Spec) { s.Writable = []string{"tmp"} },
		"relative gitdir":   func(s *Spec) { s.GitDir = "g" },
		"relative mask":     func(s *Spec) { s.Masked = []string{".env"} },
		"negative procs":    func(s *Spec) { s.MaxProcs = -1 },
	}
	for name, mut := range cases {
		s := testSpec(t, RoleBuilder)
		mut(&s)
		if _, err := m.CreateSandboxInstance(context.Background(), s); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if len(b.prepared) != 0 {
		t.Fatal("an invalid spec reached a backend")
	}
}

func TestDestroyTwiceReleasesOnce(t *testing.T) {
	m, b, _, _ := fakeManager()
	inst, _ := m.CreateSandboxInstance(context.Background(), testSpec(t, RoleBuilder))
	for i := 0; i < 2; i++ {
		if err := m.DestroySandboxInstance(context.Background(), inst.ID); err != nil {
			t.Fatal(err)
		}
	}
	if len(b.released) != 1 {
		t.Fatalf("released %d times", len(b.released))
	}
	if err := m.DestroySandboxInstance(context.Background(), "nope"); err != nil {
		t.Fatalf("unknown id must be a no-op: %v", err)
	}
}

func TestDestroyReturnsTheReleaseError(t *testing.T) {
	m, b, _, _ := fakeManager()
	b.releaseErr = errBoom
	inst, _ := m.CreateSandboxInstance(context.Background(), testSpec(t, RoleBuilder))
	if err := m.DestroySandboxInstance(context.Background(), inst.ID); !errors.Is(err, errBoom) {
		t.Fatalf("got %v", err)
	}
	if err := m.DestroySandboxInstance(context.Background(), inst.ID); err != nil {
		t.Fatalf("a destroyed instance stays destroyed: %v", err)
	}
}

func TestCommandNeverRunsUnwrapped(t *testing.T) {
	m, b, _, _ := fakeManager()
	inst, _ := m.CreateSandboxInstance(context.Background(), testSpec(t, RoleBuilder))
	const prefix = "error: the sandbox could not start the command: "

	_, err := m.Command(context.Background(), "missing", Command{Argv: []string{"true"}})
	var se *StartError
	if !errors.As(err, &se) || !strings.HasPrefix(err.Error(), prefix) {
		t.Fatalf("unknown id: %v", err)
	}
	b.commandErr = errBoom
	cmd, err := m.Command(context.Background(), inst.ID, Command{Argv: []string{"true"}})
	if cmd != nil || !errors.As(err, &se) || !errors.Is(err, errBoom) || err.Error() != prefix+"boom" {
		t.Fatalf("backend error: cmd %v err %v", cmd, err)
	}
	b.commandErr = nil
	if _, err := m.Command(context.Background(), inst.ID, Command{}); !errors.As(err, &se) {
		t.Fatalf("empty argv: %v", err)
	}
	if _, err := m.Command(context.Background(), inst.ID, Command{Argv: []string{"true"}}); err != nil {
		t.Fatal(err)
	}
	if err := m.DestroySandboxInstance(context.Background(), inst.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Command(context.Background(), inst.ID, Command{Argv: []string{"true"}}); !errors.As(err, &se) {
		t.Fatalf("a destroyed instance must refuse commands: %v", err)
	}
}

func TestEndSetupTurnsTheNetworkOffForTheNextCommand(t *testing.T) {
	m, b, d, _ := fakeManager()
	s := testSpec(t, RoleQA)
	s.Network = true
	inst, _ := m.CreateSandboxInstance(context.Background(), s)
	run := Command{Argv: []string{"true"}}
	if _, err := m.Command(context.Background(), inst.ID, run); err != nil {
		t.Fatal(err)
	}
	if !d.commanded[0].Network {
		t.Fatal("the setup command must see the network on")
	}
	if err := m.EndSetup(context.Background(), inst.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.EndSetup(context.Background(), inst.ID); err != nil {
		t.Fatalf("a second EndSetup must do nothing: %v", err)
	}
	if _, err := m.Command(context.Background(), inst.ID, run); err != nil {
		t.Fatal(err)
	}
	if d.commanded[1].Network || len(d.cut) != 1 {
		t.Fatalf("network still on or cut %d times", len(d.cut))
	}
	if len(b.cut) != 0 {
		t.Fatal("the builder backend must be untouched")
	}
	if err := m.EndSetup(context.Background(), "missing"); err == nil {
		t.Fatal("EndSetup on an unknown instance must fail")
	}
}

func TestEndSetupWorksWithoutACutter(t *testing.T) {
	f := &fakeBackend{name: "plain"}
	m := NewManager(map[string]Backend{RoleBuilder: plainOnly{f}}, nil)
	s := testSpec(t, RoleBuilder)
	s.Network = true
	inst, err := m.CreateSandboxInstance(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.EndSetup(context.Background(), inst.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Command(context.Background(), inst.ID, Command{Argv: []string{"true"}}); err != nil ||
		f.commanded[0].Network {
		t.Fatalf("err %v", err)
	}
	if len(f.cut) != 0 {
		t.Fatal("no cutter was reachable")
	}
}

func TestSetMaskedReachesTheNextCommand(t *testing.T) {
	m, b, _, _ := fakeManager()
	s := testSpec(t, RoleBuilder)
	inst, _ := m.CreateSandboxInstance(context.Background(), s)
	env := filepath.Join(s.Worktree, ".env")
	if err := m.SetMasked(inst.ID, []string{env}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Command(context.Background(), inst.ID, Command{Argv: []string{"true"}}); err != nil {
		t.Fatal(err)
	}
	if got := b.commanded[0].Masked; len(got) != 1 || got[0] != env {
		t.Fatalf("masked %v", got)
	}
	if err := m.SetMasked(inst.ID, []string{".env"}); err == nil {
		t.Fatal("a relative mask must be refused")
	}
	if err := m.SetMasked("missing", nil); err == nil {
		t.Fatal("unknown instance must fail")
	}
}

func TestCaveatsListTheLimitsABackendCannotEnforce(t *testing.T) {
	m, b, _, _ := fakeManager()
	b.caveats = []string{"no process cap"}
	inst, _ := m.CreateSandboxInstance(context.Background(), testSpec(t, RoleBuilder))
	if got := m.Caveats(inst.ID); len(got) != 1 || got[0] != "no process cap" {
		t.Fatalf("got %v", got)
	}
	if got := m.Caveats("missing"); got != nil {
		t.Fatalf("got %v", got)
	}
}

func TestConcurrentCreateAndDestroy(t *testing.T) {
	m, _, _, _ := fakeManager()
	specs := make([]Spec, 16)
	for i := range specs {
		specs[i] = testSpec(t, RoleBuilder)
	}
	var wg sync.WaitGroup
	for _, s := range specs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inst, err := m.CreateSandboxInstance(context.Background(), s)
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := m.Command(context.Background(), inst.ID, Command{Argv: []string{"true"}}); err != nil {
				t.Error(err)
			}
			_ = m.DestroySandboxInstance(context.Background(), inst.ID)
			_ = m.DestroySandboxInstance(context.Background(), inst.ID)
		}()
	}
	wg.Wait()
}
