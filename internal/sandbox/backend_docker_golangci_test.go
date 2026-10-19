package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// golangciRepo makes the worktree a Go repository with the config file, and puts a fake golangci-lint that prints version
// for "version --short" on PATH when version is not empty.
func golangciRepo(t *testing.T, s Spec, config, version string) {
	t.Helper()
	goRepo(t, s)

	if config != "" {
		if err := os.WriteFile(filepath.Join(s.Worktree, config), []byte("version: \"2\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if version != "" {
		putFake(t, "golangci-lint", `[ "$1 $2" = "version --short" ] && echo `+version+"; exit 0")
	}
}

// onlyDockerOnPath makes the fake docker the only program on PATH, so the host has no golangci-lint.
func onlyDockerOnPath(t *testing.T) {
	t.Helper()

	p, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", filepath.Dir(p))
}

func TestDockerInstallsTheHostGolangciLintVersionAsRoot(t *testing.T) {
	calls := fakeDocker(t)
	s := testSpec(t, RoleTester)
	s.Network = true
	golangciRepo(t, s, ".golangci.yml", "2.14.0")

	inst, err := NewDocker(DockerOptions{}).Prepare(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}

	want := "exec -u 0 komodo-g1-" + inst.ID[:8] + " env GOBIN=/usr/local/bin go install " +
		"github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0"
	if exec := calls().verb("exec"); len(exec) != 1 || strings.Join(exec[0], " ") != want {
		t.Fatalf("exec calls %v", exec)
	}
}

func TestDockerInstallsNothingWithoutTheConfig(t *testing.T) {
	calls := fakeDocker(t)
	s := testSpec(t, RoleTester)
	golangciRepo(t, s, "", "2.14.0")

	if _, err := NewDocker(DockerOptions{}).Prepare(context.Background(), s); err != nil {
		t.Fatalf("a repository that lints with go vet needs no network: %v", err)
	}
	if n := len(calls().verb("exec")); n != 0 {
		t.Fatalf("%d tool additions ran", n)
	}
}

func TestDockerInstallsNothingWithoutTheHostTool(t *testing.T) {
	calls := fakeDocker(t)
	s := testSpec(t, RoleTester)
	golangciRepo(t, s, ".golangci.yml", "")
	onlyDockerOnPath(t)

	if _, err := NewDocker(DockerOptions{}).Prepare(context.Background(), s); err != nil {
		t.Fatalf("a repository that lints with go vet needs no network: %v", err)
	}
	if n := len(calls().verb("exec")); n != 0 {
		t.Fatalf("%d tool additions ran", n)
	}
}

func TestDockerGolangciLintNeedsAReadableHostVersion(t *testing.T) {
	calls := fakeDocker(t)
	s := testSpec(t, RoleTester)
	s.Network = true
	golangciRepo(t, s, ".golangci.yml", "not-a-version")

	_, err := NewDocker(DockerOptions{}).Prepare(context.Background(), s)
	if err == nil || !strings.Contains(err.Error(), "sandbox.image") {
		t.Fatalf("got %v", err)
	}
	if n := len(calls().verb("create")); n != 0 {
		t.Fatalf("%d containers made before the version was known", n)
	}
}

func TestDockerGolangciLintNeedsTheNetwork(t *testing.T) {
	fakeDocker(t)
	s := testSpec(t, RoleTester)
	golangciRepo(t, s, ".golangci.yaml", "2.14.0")

	if _, err := NewDocker(DockerOptions{}).Prepare(context.Background(), s); err == nil ||
		!strings.Contains(err.Error(), "network") {
		t.Fatalf("got %v", err)
	}
}

func TestDockerCustomImageGetsNoGolangciLint(t *testing.T) {
	calls := fakeDocker(t)
	s := testSpec(t, RoleTester)
	golangciRepo(t, s, ".golangci.yml", "2.14.0")

	if _, err := NewDocker(DockerOptions{Image: "my/go:1"}).Prepare(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if n := len(calls().verb("exec")); n != 0 {
		t.Fatalf("%d tool additions ran in a custom image", n)
	}
}

func TestGolangciModule(t *testing.T) {
	for v, want := range map[string]string{
		"2.14.0": "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0",
		"3.0.1":  "github.com/golangci/golangci-lint/v3/cmd/golangci-lint@v3.0.1",
		"1.64.8": "github.com/golangci/golangci-lint/cmd/golangci-lint@v1.64.8",
	} {
		if got := golangciModule(v); got != want {
			t.Errorf("%s: %s", v, got)
		}
	}
}
