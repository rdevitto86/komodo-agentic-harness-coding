package task

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// goSetupRig returns a rig whose setup command is argv over a host module cache that holds a download cache when withCache is
// true, with the host Go settings pinned.
func goSetupRig(t *testing.T, lang string, argv []string, withCache bool) (*pipeRig, string) {
	t.Helper()

	old := hostGoEnv
	hostGoEnv = func(context.Context) (string, string) { return "https://proxy.example/x,direct", "github.com/me/*" }
	t.Cleanup(func() { hostGoEnv = old })

	mod := t.TempDir()
	if withCache {
		if err := os.MkdirAll(filepath.Join(mod, "cache", "download"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	r := newPipeRig(t, rigOpts{mut: func(_ *GroupSpec, c *Collaborators) {
		c.Language, c.Plan.Modules = lang, mod
		if argv != nil {
			c.Commands["setup"] = argv
		}
	}})

	return r, mod
}

func TestAGoSetupCommandGetsTheHostDownloadCacheAsAFileProxy(t *testing.T) {
	r, mod := goSetupRig(t, "go", []string{"true"}, true)
	if _, err := r.m.instance(context.Background(), "builder"); err != nil {
		t.Fatal(err)
	}

	dl := filepath.Join(mod, "cache", "download")
	got := r.be.prepared[0]
	if !got.Network || got.Modules != mod {
		t.Fatalf("spec network %v modules %q, want the module cache %s that holds %s", got.Network, got.Modules, mod, dl)
	}

	for _, want := range []string{
		"GOPROXY=file://" + dl + ",https://proxy.example/x,direct", "GONOPROXY=", "GOPRIVATE=",
		"GONOSUMDB=github.com/me/*", "GIT_TERMINAL_PROMPT=0",
	} {
		if !slices.Contains(got.SetupEnv, want) {
			t.Errorf("setup env lacks %q: %v", want, got.SetupEnv)
		}
	}
}

func TestSetupWithoutAHostDownloadCacheRunsAsBefore(t *testing.T) {
	r, _ := goSetupRig(t, "go", []string{"true"}, false)
	if _, err := r.m.instance(context.Background(), "builder"); err != nil {
		t.Fatal(err)
	}

	got := r.be.prepared[0]
	if !got.Network || len(got.SetupEnv) != 0 {
		t.Fatalf("spec %+v", got)
	}
}

func TestOnlyAGoRepoSetupGetsTheDownloadCache(t *testing.T) {
	r, _ := goSetupRig(t, "rust", []string{"true"}, true)
	if _, err := r.m.instance(context.Background(), "builder"); err != nil {
		t.Fatal(err)
	}
	if got := r.be.prepared[0]; len(got.SetupEnv) != 0 {
		t.Fatalf("a rust repo's spec %+v", got)
	}

	r, _ = goSetupRig(t, "go", nil, true)
	delete(r.m.c.Commands, "setup")

	if _, err := r.m.instance(context.Background(), "builder"); err != nil {
		t.Fatal(err)
	}
	if got := r.be.prepared[0]; got.Network || len(got.SetupEnv) != 0 {
		t.Fatalf("a repo with no setup command has spec %+v", got)
	}
}

func TestAModuleFailureInSetupCarriesTheFixLine(t *testing.T) {
	failing := []string{
		"sh", "-c", "echo \"could not read Username for 'https://github.com': terminal prompts disabled\"; exit 1",
	}
	r, _ := goSetupRig(t, "go", failing, true)

	_, err := r.m.instance(context.Background(), "builder")

	var be *blockErr
	if !errors.As(err, &be) || be.reason != "setup" {
		t.Fatalf("error %v, want a setup block", err)
	}

	d, _ := be.detail.(map[string]string)
	if !strings.Contains(d["fix"], "go mod download") || !strings.Contains(d["fix"], "komodo run tg-1") {
		t.Fatalf("detail %v lacks the fix line", d)
	}
}

func TestAFailedSetupDestroysItsInstanceAndTheNextCallRunsSetupOnANewOne(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	flaky := []string{"sh", "-c", "if [ -e " + marker + " ]; then exit 0; fi; touch " + marker + "; exit 1"}
	r, _ := goSetupRig(t, "go", flaky, true)

	_, err := r.m.instance(context.Background(), "builder")

	var be *blockErr
	if !errors.As(err, &be) || be.reason != "setup" {
		t.Fatalf("error %v, want a setup block", err)
	}
	if !slices.Equal(r.be.released, []string{"inst-1"}) {
		t.Fatalf("destroyed instances %v after a failed setup, want [inst-1]", r.be.released)
	}

	id, err := r.m.instance(context.Background(), "builder")
	if err != nil {
		t.Fatal(err)
	}

	if id != "inst-2" || len(r.be.prepared) != 2 {
		t.Fatalf("instance %q and %d prepared, want a new instance inst-2 and 2 prepared", id, len(r.be.prepared))
	}
	if !r.be.prepared[1].Network {
		t.Fatal("the retry's instance was made without the network its setup needs")
	}
	if again, _ := r.m.instance(context.Background(), "builder"); again != id {
		t.Fatalf("a finished setup made %q on the next call, want %q reused", again, id)
	}
}

func TestAnUnrelatedSetupFailureCarriesNoFixLine(t *testing.T) {
	r, _ := goSetupRig(t, "go", []string{"sh", "-c", "echo out of disk; exit 1"}, true)

	_, err := r.m.instance(context.Background(), "builder")

	var be *blockErr
	if !errors.As(err, &be) {
		t.Fatalf("error %v, want a setup block", err)
	}
	if d, _ := be.detail.(map[string]string); d["fix"] != "" {
		t.Fatalf("detail %v holds a fix line", d)
	}
}
