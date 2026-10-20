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

// fillCall is one host download the fake runner saw, with how many instances existed at that moment.
type fillCall struct {
	dir       string
	env       []string
	instances int
}

// fillRig returns a rig whose host download is recorded and answers with fail; goMod and modules choose what the worktree and
// the spec hold.
func fillRig(t *testing.T, goMod, modules bool, fail error) (*pipeRig, *[]fillCall) {
	t.Helper()

	var calls []fillCall

	var r *pipeRig

	old := hostGoDownload
	hostGoDownload = func(_ context.Context, dir string, env []string) error {
		calls = append(calls, fillCall{dir: dir, env: env, instances: len(r.be.prepared)})
		return fail
	}
	t.Cleanup(func() { hostGoDownload = old })

	mod := t.TempDir()
	r = newPipeRig(t, rigOpts{mut: func(_ *GroupSpec, c *Collaborators) {
		c.Language = "go"
		if modules {
			c.Plan.Modules = mod
		} else {
			c.Plan.Modules = ""
		}
	}})

	if goMod {
		writeGoMod(t, r.wt, "module x\n")
	}

	return r, &calls
}

// writeGoMod writes a go.mod holding body at the root of dir.
func writeGoMod(t *testing.T, dir, body string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTheHostFillsTheModuleCacheBeforeTheInstanceIsCreated(t *testing.T) {
	r, calls := fillRig(t, true, true, nil)
	if _, err := r.m.instance(context.Background(), "builder"); err != nil {
		t.Fatal(err)
	}

	if len(*calls) != 1 {
		t.Fatalf("%d host downloads, want 1", len(*calls))
	}

	got := (*calls)[0]
	if got.dir != r.wt || got.instances != 0 {
		t.Fatalf("download in %q with %d instances made, want %q and 0", got.dir, got.instances, r.wt)
	}
	if len(r.be.prepared) != 1 {
		t.Fatalf("%d instances prepared, want 1", len(r.be.prepared))
	}
}

func TestTheHostFillKeepsTheToolchainLocalAndModulesMutable(t *testing.T) {
	t.Setenv("GOFLAGS", "-mod=vendor")
	t.Setenv("KOMODO_FILL_PROBE", "kept")

	r, calls := fillRig(t, true, true, nil)
	if _, err := r.m.instance(context.Background(), "builder"); err != nil {
		t.Fatal(err)
	}

	if len(*calls) != 1 {
		t.Fatalf("%d host downloads, want 1", len(*calls))
	}

	env := (*calls)[0].env
	for _, want := range []string{"GOTOOLCHAIN=local", "GOFLAGS=-mod=mod", "KOMODO_FILL_PROBE=kept"} {
		if !slices.Contains(env, want) {
			t.Errorf("fill env lacks %q", want)
		}
	}
	if i := slices.Index(env, "GOFLAGS=-mod=vendor"); i >= 0 && i > slices.Index(env, "GOFLAGS=-mod=mod") {
		t.Error("the host's GOFLAGS comes after the fill's, so it would win")
	}
}

func TestTheHostFillIsSkippedWithoutAGoModOrAModuleCache(t *testing.T) {
	for name, c := range map[string]struct{ goMod, modules bool }{
		"no go.mod":      {false, true},
		"no module path": {true, false},
	} {
		t.Run(name, func(t *testing.T) {
			r, calls := fillRig(t, c.goMod, c.modules, nil)
			if _, err := r.m.instance(context.Background(), "builder"); err != nil {
				t.Fatal(err)
			}
			if len(*calls) != 0 {
				t.Fatalf("%d host downloads, want 0", len(*calls))
			}
		})
	}
}

func TestTheHostFillRunsOncePerGroupAndAgainWhenGoModChanges(t *testing.T) {
	r, calls := fillRig(t, true, true, nil)
	for _, role := range []string{"builder", "tester"} {
		if _, err := r.m.instance(context.Background(), role); err != nil {
			t.Fatal(err)
		}
	}
	if len(*calls) != 1 {
		t.Fatalf("%d host downloads for two roles, want 1", len(*calls))
	}

	writeGoMod(t, r.wt, "module x\n\nrequire github.com/kr/text v0.1.0\n")

	if _, err := r.m.instance(context.Background(), "reviewer"); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 {
		t.Fatalf("%d host downloads after go.mod changed, want 2", len(*calls))
	}

	if err := os.WriteFile(filepath.Join(r.wt, "go.sum"), []byte("github.com/kr/text v0.1.0 h1:x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r.m.mu.Lock()
	delete(r.m.instances, "reviewer")
	r.m.mu.Unlock()

	if _, err := r.m.instance(context.Background(), "reviewer"); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 3 {
		t.Fatalf("%d host downloads after go.sum changed, want 3", len(*calls))
	}
}

func TestAFailedHostFillBlocksSetupWithTheFixLineAndMakesNoInstance(t *testing.T) {
	r, calls := fillRig(t, true, true, errors.New("go mod download: github.com/me/x: terminal prompts disabled"))

	_, err := r.m.instance(context.Background(), "builder")

	var be *blockErr
	if !errors.As(err, &be) || be.reason != "setup" {
		t.Fatalf("error %v, want a setup block", err)
	}

	d, _ := be.detail.(map[string]string)
	if d["role"] != "builder" || !strings.Contains(d["output"], "terminal prompts disabled") {
		t.Fatalf("detail %v lacks the role or the output", d)
	}
	if !strings.Contains(d["fix"], "go mod download") || !strings.Contains(d["fix"], "komodo run tg-1") {
		t.Fatalf("detail %v lacks the fix line", d)
	}
	if len(r.be.prepared) != 0 || len(r.be.released) != 0 {
		t.Fatalf("%d instances prepared and %d destroyed, want none", len(r.be.prepared), len(r.be.released))
	}

	if _, err := r.m.instance(context.Background(), "builder"); err == nil || len(*calls) != 2 {
		t.Fatalf("the next call made %d downloads and error %v, want a second try that fails again", len(*calls), err)
	}
}

func TestAReadOnlyModuleCacheWriteInSetupCarriesTheFixLine(t *testing.T) {
	failing := []string{"sh", "-c", "echo 'mkdir /go/pkg/mod/cache: read-only file system'; exit 1"}
	r, _ := goSetupRig(t, "go", failing, true)

	_, err := r.m.instance(context.Background(), "builder")

	var be *blockErr
	if !errors.As(err, &be) {
		t.Fatalf("error %v, want a setup block", err)
	}
	if d, _ := be.detail.(map[string]string); !strings.Contains(d["fix"], "go mod download") {
		t.Fatalf("detail %v lacks the fix line", d)
	}
}
