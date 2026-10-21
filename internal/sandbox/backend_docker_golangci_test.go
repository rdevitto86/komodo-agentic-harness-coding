package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGolangci puts a fake golangci-lint on PATH that answers the way one release line does: "v2" reads "version --short",
// "v1" reads "version --format short" and rejects "--short", and "banner" reads only "--version".
func fakeGolangci(t *testing.T, line, version string) {
	t.Helper()

	script := map[string]string{
		"v2": `[ "$1 $2" = "version --short" ] && echo ` + version + "; exit 0",
		"v1": `[ "$1 $2 $3" = "version --format short" ] && echo ` + version + "; exit 0",
		"banner": `[ "$1" = "--version" ] && echo "golangci-lint has version ` + version +
			` built with go1.24 from abc on 2025-01-01T00:00:00Z"; exit 0`,
	}[line]

	putFake(t, "golangci-lint", script+"\necho 'unknown flag' >&2; exit 2")
}

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
		fakeGolangci(t, "v2", version)
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
	s := testSpec(t, RoleQA)
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

func TestDockerReadsTheHostVersionFromEveryReleaseLine(t *testing.T) {
	for _, tc := range []struct{ line, version, module string }{
		{"v2", "2.14.0", "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0"},
		{"v1", "1.64.8", "github.com/golangci/golangci-lint/cmd/golangci-lint@v1.64.8"},
		{"banner", "v1.55.2", "github.com/golangci/golangci-lint/cmd/golangci-lint@v1.55.2"},
	} {
		t.Run(tc.line, func(t *testing.T) {
			calls := fakeDocker(t)
			s := testSpec(t, RoleQA)
			s.Network = true
			golangciRepo(t, s, ".golangci.yml", "")
			fakeGolangci(t, tc.line, tc.version)

			inst, err := NewDocker(DockerOptions{}).Prepare(context.Background(), s)
			if err != nil {
				t.Fatal(err)
			}

			want := "exec -u 0 komodo-g1-" + inst.ID[:8] + " env GOBIN=/usr/local/bin go install " + tc.module
			if exec := calls().verb("exec"); len(exec) != 1 || strings.Join(exec[0], " ") != want {
				t.Fatalf("exec calls %v", exec)
			}
		})
	}
}

func TestDockerInstallsNothingWithoutTheConfig(t *testing.T) {
	calls := fakeDocker(t)
	s := testSpec(t, RoleQA)
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
	s := testSpec(t, RoleQA)
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
	s := testSpec(t, RoleQA)
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
	s := testSpec(t, RoleQA)
	golangciRepo(t, s, ".golangci.yaml", "2.14.0")

	if _, err := NewDocker(DockerOptions{}).Prepare(context.Background(), s); err == nil ||
		!strings.Contains(err.Error(), "network") {
		t.Fatalf("got %v", err)
	}
}

func TestDockerCustomImageGetsNoGolangciLint(t *testing.T) {
	calls := fakeDocker(t)
	s := testSpec(t, RoleQA)
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
