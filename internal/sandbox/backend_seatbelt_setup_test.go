//go:build unix

package sandbox

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSeatbeltSetupDownloadsFromTheHostCacheLive(t *testing.T) {
	if os.Getenv("KOMODO_TEST_SANDBOX_EXEC") == "" {
		t.Skip("set KOMODO_TEST_SANDBOX_EXEC=1 to run real sandbox-exec commands")
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec is not installed")
	}

	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not installed")
	}

	s := testSpec(t, RoleBuilder)
	root := filepath.Dir(s.Worktree)

	t.Cleanup(func() { _ = exec.Command("chmod", "-R", "u+w", root).Run() })

	hostMod := filepath.Join(root, "hostmod")
	origin := filepath.Join(root, "origin", "example.com", "priv", "m", "@v")
	writeFakeModule(t, origin)

	files := map[string]string{
		"go.mod":  "module example.com/app\n\ngo 1.21\n\nrequire example.com/priv/m v1.0.0\n",
		"main.go": "package main\n\nimport \"example.com/priv/m\"\n\nfunc main() { _ = m.Hello }\n",
	}
	writeTree(t, s.Worktree, files)

	// The host step fills a download cache and go.sum, the way a person's earlier `go mod download` did.
	for _, args := range [][]string{{"mod", "tidy"}, {"mod", "download"}} {
		host := exec.Command(goBin, args...)
		host.Dir = s.Worktree
		host.Env = append(
			os.Environ(),
			"GOMODCACHE="+hostMod, "GOPROXY=file://"+filepath.Join(root, "origin"), "GOSUMDB=off",
			"GOFLAGS=-modcacherw", "GOTOOLCHAIN=local", "GOPRIVATE=", "GOENV=off",
		)
		if out, err := host.CombinedOutput(); err != nil {
			t.Fatalf("fill the host cache with go %v: %v\n%s", args, err, out)
		}
	}

	goroot, err := exec.Command(goBin, "env", "GOROOT").Output()
	if err != nil {
		t.Fatal(err)
	}

	s.Toolchains = []string{strings.TrimSpace(string(goroot))}
	s.Modules = hostMod
	s.Network = true
	s.SetupEnv = GoSetupEnv(hostMod, "off", "example.com/priv/*")

	if len(s.SetupEnv) == 0 {
		t.Fatalf("no setup variables for %s", hostMod)
	}

	m := NewManager(map[string]Backend{RoleBuilder: NewSeatbelt(SeatbeltOptions{Home: root})}, nil)

	inst, err := m.CreateSandboxInstance(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = m.DestroySandboxInstance(context.Background(), inst.ID) })

	run := func(argv ...string) (string, error) {
		t.Helper()

		cmd, err := m.Command(context.Background(), inst.ID, Command{
			Argv:    argv,
			Env:     []string{"GOCACHE=" + s.Writable[1]},
			Timeout: 120 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}

		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	dl := filepath.Join(hostMod, "cache", "download")
	if out, err := run("/bin/sh", "-c", "ls "+dl); err != nil {
		t.Errorf("the host download cache must be readable during setup: %v %s", err, out)
	}
	if out, err := run("/bin/sh", "-c", "touch "+filepath.Join(dl, "x")); err == nil {
		t.Errorf("a write to the host download cache must fail: %s", out)
	}
	if _, err := os.Stat(filepath.Join(dl, "x")); err == nil {
		t.Error("the sandbox wrote into the host download cache")
	}
	if out, err := run("go", "mod", "download"); err != nil {
		t.Fatalf("the setup download must come from the host cache: %v\n%s", err, out)
	}
	if err := m.EndSetup(context.Background(), inst.ID); err != nil {
		t.Fatal(err)
	}
	if out, err := run("go", "build", "-buildvcs=false", "./..."); err != nil {
		t.Errorf("the build after setup must pass offline: %v\n%s", err, out)
	}
}

// writeFakeModule writes a one-version module in the layout of a Go module proxy.
func writeFakeModule(t *testing.T, dir string) {
	t.Helper()

	mod := "module example.com/priv/m\n\ngo 1.21\n"

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"example.com/priv/m@v1.0.0/go.mod": mod,
		"example.com/priv/m@v1.0.0/m.go":   "package m\n\nconst Hello = \"hi\"\n",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"list":        []byte("v1.0.0\n"),
		"v1.0.0.info": []byte(`{"Version":"v1.0.0","Time":"2024-01-01T00:00:00Z"}`),
		"v1.0.0.mod":  []byte(mod),
		"v1.0.0.zip":  buf.Bytes(),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
