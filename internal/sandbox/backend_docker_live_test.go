package sandbox

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A real container built from the golang image lints a worktree with the host's golangci-lint version; it skips without
// a daemon, the image, the host tool or the network.
func TestDockerLiveGolangciLint(t *testing.T) {
	if _, err := exec.LookPath("golangci-lint"); err != nil {
		t.Skip("golangci-lint is not on PATH")
	}

	d := NewDocker(DockerOptions{})
	if err := d.Check(); err != nil {
		t.Skipf("no docker daemon: %v", err)
	}

	s := testSpec(t, RoleTester)
	s.Network = true
	goRepo(t, s)

	ver := strings.TrimSpace(string(mustOutput(t, "go", "env", "GOVERSION")))
	minor := strings.Join(strings.SplitN(strings.TrimPrefix(ver, "go"), ".", 3)[:2], ".")
	if err := os.WriteFile(
		filepath.Join(s.Worktree, "go.mod"),
		[]byte("module x\n\ngo "+minor+"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("docker", "image", "inspect", "golang:"+minor).Run(); err != nil {
		t.Skipf("image golang:%s is not pulled", minor)
	}

	conn, err := net.DialTimeout("tcp", "proxy.golang.org:443", 3*time.Second)
	if err != nil {
		t.Skipf("offline: %v", err)
	}
	_ = conn.Close()

	if err := os.WriteFile(
		filepath.Join(s.Worktree, ".golangci.yml"),
		[]byte("version: \"2\"\nlinters:\n  enable:\n    - lll\n  settings:\n    lll:\n      line-length: 40\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	long := "package x\n\n// Long is a comment far longer than the forty columns the configuration allows.\nfunc Long() {}\n"
	short := "package x\n\n// Long does nothing.\nfunc Long() {}\n"
	file := filepath.Join(s.Worktree, "x.go")
	if err := os.WriteFile(file, []byte(long), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	inst, err := d.Prepare(ctx, s)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	t.Cleanup(func() { _ = d.Release(context.WithoutCancel(ctx), inst) })

	if err := d.CutNetwork(ctx, inst); err != nil {
		t.Fatal(err)
	}

	s.Network = false
	lint := func() (string, error) {
		cmd, err := d.Command(ctx, inst, s, Command{Argv: []string{"golangci-lint", "run"}})
		if err != nil {
			t.Fatal(err)
		}

		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	if out, err := lint(); err == nil || !strings.Contains(out, "lll") {
		t.Fatalf("the long line must fail the lint before the fix: err %v, output %s", err, out)
	}
	if err := os.WriteFile(file, []byte(short), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := lint(); err != nil {
		t.Fatalf("the fixed file must pass: %v\n%s", err, out)
	}
}

// mustOutput runs a program and returns its output.
func mustOutput(t *testing.T, name string, args ...string) []byte {
	t.Helper()

	out, err := exec.Command(name, args...).Output()
	if err != nil {
		t.Skipf("%s: %v", name, err)
	}

	return out
}
