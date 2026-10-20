package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hostCache makes a module cache folder that holds a download cache and returns the module cache path.
func hostCache(t *testing.T) string {
	t.Helper()

	mod := realPath(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(mod, "cache", "download"), 0o755); err != nil {
		t.Fatal(err)
	}

	return mod
}

func TestGoSetupAccessPointsTheFileProxyAtTheDownloadCache(t *testing.T) {
	mod := hostCache(t)
	read, env := GoSetupAccess(mod, "https://proxy.golang.org,direct", "github.com/rdevitto86/*,example.com/x")

	dl := filepath.Join(mod, "cache", "download")
	if len(read) != 1 || read[0] != dl {
		t.Fatalf("readable %v, want [%s]", read, dl)
	}

	want := []string{
		"GOPROXY=file://" + dl + ",https://proxy.golang.org,direct",
		"GONOPROXY=",
		"GOPRIVATE=",
		"GONOSUMDB=github.com/rdevitto86/*,example.com/x",
		"GIT_TERMINAL_PROMPT=0",
	}
	if strings.Join(env, "\n") != strings.Join(want, "\n") {
		t.Fatalf("env\n%s\nwant\n%s", strings.Join(env, "\n"), strings.Join(want, "\n"))
	}
}

func TestGoSetupAccessDefaultsTheHostProxy(t *testing.T) {
	mod := hostCache(t)
	_, env := GoSetupAccess(mod, "", "")
	if !contains(env, "GOPROXY=file://"+filepath.Join(mod, "cache", "download")+",https://proxy.golang.org") {
		t.Fatalf("env %v", env)
	}
	if !contains(env, "GONOSUMDB=") {
		t.Fatalf("env %v", env)
	}
}

func TestGoSetupAccessEscapesTheCachePath(t *testing.T) {
	mod := filepath.Join(realPath(t, t.TempDir()), "my cache")
	if err := os.MkdirAll(filepath.Join(mod, "cache", "download"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, env := GoSetupAccess(mod, "", "")
	if !strings.Contains(env[0], "my%20cache") {
		t.Fatalf("GOPROXY %q does not escape the space", env[0])
	}
}

func TestGoSetupAccessIsEmptyWithoutAHostCache(t *testing.T) {
	missing := filepath.Join(realPath(t, t.TempDir()), "nothing")
	for _, mod := range []string{"", missing} {
		if read, env := GoSetupAccess(mod, "", ""); read != nil || env != nil {
			t.Errorf("module cache %q gave %v %v", mod, read, env)
		}
	}
}

func TestNewRulesAddsSetupAccessOnlyWhileTheNetworkIsOn(t *testing.T) {
	mod := hostCache(t)
	read, env := GoSetupAccess(mod, "", "")
	s := fakeSpec()
	s.SetupReadable, s.SetupEnv = read, env

	off := mustRules(t, s)
	if contains(off.Readable, read[0]) || len(off.Env) != 0 {
		t.Fatalf("network off: readable %v env %v", off.Readable, off.Env)
	}

	s.Network = true

	on := mustRules(t, s)
	if !contains(on.Readable, read[0]) || len(on.Env) != len(env) {
		t.Fatalf("network on: readable %v env %v", on.Readable, on.Env)
	}
}

func TestSetupReadableFollowsTheSamePathChecks(t *testing.T) {
	s := fakeSpec()
	s.Network, s.SetupReadable = true, []string{"relative/cache"}
	if _, err := newRules(s); err == nil {
		t.Fatal("a relative setup path must be refused")
	}
}

func TestEnvCarriesTheSetupVariablesOnlyOnline(t *testing.T) {
	s := fakeSpec()
	s.SetupEnv = []string{"GOPROXY=file:///c,https://p", "GOPRIVATE="}

	off := buildEnv(mustRules(t, s), Command{}, false)
	if strings.Contains(strings.Join(off, "\n"), "GOPROXY") {
		t.Fatalf("network off env %v", off)
	}

	s.Network = true

	on := buildEnv(mustRules(t, s), Command{Env: []string{"GOPROXY=https://evil"}}, false)
	if !contains(on, "GOPROXY=file:///c,https://p") || !contains(on, "GOPRIVATE=") || contains(on, "GOPROXY=https://evil") {
		t.Fatalf("network on env %v", on)
	}
}

func TestSetupAccessEndsWithTheNetwork(t *testing.T) {
	m, be, _, _ := fakeManager()
	s := testSpec(t, RoleBuilder)
	s.Network = true
	s.SetupReadable, s.SetupEnv = GoSetupAccess(hostCache(t), "", "")

	inst, err := m.CreateSandboxInstance(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Command(context.Background(), inst.ID, Command{Argv: []string{"true"}}); err != nil {
		t.Fatal(err)
	}
	if got := be.commanded[len(be.commanded)-1]; len(got.SetupReadable) != 1 || len(got.SetupEnv) == 0 || !got.Network {
		t.Fatalf("the setup command's spec %+v", got)
	}
	if err := m.EndSetup(context.Background(), inst.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Command(context.Background(), inst.ID, Command{Argv: []string{"true"}}); err != nil {
		t.Fatal(err)
	}
	if got := be.commanded[len(be.commanded)-1]; got.Network {
		t.Fatalf("a later command keeps the network: %+v", got)
	}

	r := mustRules(t, be.commanded[len(be.commanded)-1])
	if len(r.Env) != 0 || contains(r.Readable, s.SetupReadable[0]) {
		t.Fatalf("a later command keeps the setup access: readable %v env %v", r.Readable, r.Env)
	}
}
