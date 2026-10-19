package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBubblewrapCheck(t *testing.T) {
	emptyPath(t)
	if err := NewBubblewrap(BubblewrapOptions{GOOS: "darwin"}).Check(); err == nil ||
		!strings.Contains(err.Error(), "Linux") {
		t.Fatalf("other system: %v", err)
	}
	if err := NewBubblewrap(BubblewrapOptions{GOOS: "linux"}).Check(); err == nil ||
		!strings.Contains(err.Error(), "bwrap") || !strings.Contains(err.Error(), "install") {
		t.Fatalf("missing program: %v", err)
	}
	putFake(t, "bwrap", `echo "bwrap: No permissions to create new namespace" >&2; exit 1`+"\n")
	err := NewBubblewrap(BubblewrapOptions{GOOS: "linux"}).Check()
	if err == nil || !strings.Contains(err.Error(), "user namespaces") || !strings.Contains(err.Error(), "No permissions") {
		t.Fatalf("blocked namespaces must name the fix: %v", err)
	}
	putFake(t, "bwrap", "exit 0\n")
	if err := NewBubblewrap(BubblewrapOptions{GOOS: "linux"}).Check(); err != nil {
		t.Fatal(err)
	}
}

func TestBubblewrapFailsClosedWithoutAHomeFolder(t *testing.T) {
	putFake(t, "bwrap", "exit 0\n")
	t.Setenv("HOME", "")
	bw := NewBubblewrap(BubblewrapOptions{GOOS: "linux"})
	if err := bw.Check(); err == nil || !strings.Contains(err.Error(), "home") {
		t.Fatalf("Check: %v", err)
	}
	if _, err := bw.Prepare(context.Background(), testSpec(t, RoleBuilder)); err == nil ||
		!strings.Contains(err.Error(), "home") {
		t.Fatalf("Prepare: %v", err)
	}
}

func TestBubblewrapCommandWrapsTheArgv(t *testing.T) {
	putFake(t, "bwrap", "exit 0\n")
	bw := NewBubblewrap(BubblewrapOptions{GOOS: "linux", Home: "/home/dev"})
	s := testSpec(t, RoleBuilder)
	inst, err := bw.Prepare(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if inst.Backend != "bubblewrap" || inst.ID == "" {
		t.Fatalf("instance %+v", inst)
	}
	cmd, err := bw.Command(context.Background(), inst, s, Command{
		Argv: []string{"go", "vet", "./..."},
		Env:  []string{"GOCACHE=" + s.Writable[1], "TOKEN=x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(cmd.Path) != "bwrap" {
		t.Fatalf("path %s", cmd.Path)
	}
	args := cmd.Args[1:]
	sep := indexOf(args, "--")
	if sep < 0 || strings.Join(args[sep:], " ") != "-- go vet ./..." {
		t.Fatalf("args %v", args)
	}
	if indexOf(args, "--unshare-net") < 0 || indexOf(args, "--chdir", s.Worktree) < 0 {
		t.Fatalf("args %v", args)
	}
	if !contains(cmd.Env, "HOME="+s.Writable[0]) || contains(cmd.Env, "TOKEN=x") || !reflect.DeepEqual(cmd.SysProcAttr, ownGroup()) {
		t.Fatalf("env %v", cmd.Env)
	}
}

func TestBubblewrapSkipsFoldersThatDoNotExist(t *testing.T) {
	putFake(t, "bwrap", "exit 0\n")
	bw := NewBubblewrap(BubblewrapOptions{GOOS: "linux", Home: "/home/dev"})
	s := testSpec(t, RoleBuilder)
	s.Toolchains = append(s.Toolchains, filepath.Join(filepath.Dir(s.Worktree), "no-such-toolchain"))
	s.Masked = []string{filepath.Join(s.Worktree, ".env")}
	inst, _ := bw.Prepare(context.Background(), s)
	cmd, err := bw.Command(context.Background(), inst, s, Command{Argv: []string{"true"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range cmd.Args {
		if strings.Contains(a, "no-such-toolchain") || strings.HasSuffix(a, ".env") {
			t.Fatalf("a missing path was mounted: %s", a)
		}
	}
	if err := os.WriteFile(s.Masked[0], []byte("K=v"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd, _ = bw.Command(context.Background(), inst, s, Command{Argv: []string{"true"}})
	if indexOf(cmd.Args, "--ro-bind", "/dev/null", s.Masked[0]) < 0 {
		t.Fatalf("an existing masked file must be covered: %v", cmd.Args)
	}
}

func TestBubblewrapNetworkFollowsTheSpec(t *testing.T) {
	putFake(t, "bwrap", "exit 0\n")
	bw := NewBubblewrap(BubblewrapOptions{GOOS: "linux", Home: "/home/dev"})
	s := testSpec(t, RoleBuilder)
	s.Network = true
	inst, _ := bw.Prepare(context.Background(), s)
	on, _ := bw.Command(context.Background(), inst, s, Command{Argv: []string{"true"}})
	s.Network = false
	off, _ := bw.Command(context.Background(), inst, s, Command{Argv: []string{"true"}})
	if contains(on.Args, "--unshare-net") || !contains(off.Args, "--unshare-net") {
		t.Fatal("--unshare-net must be left out for the setup command only")
	}
}

func TestBubblewrapCaveatsNameTheMissingCaps(t *testing.T) {
	bw := NewBubblewrap(BubblewrapOptions{})
	if got := bw.Caveats(fakeSpec()); len(got) != 2 {
		t.Fatalf("both caps are missing without delegation: %v", got)
	}
}
