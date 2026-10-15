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

const fakeSandboxExec = `
{ for a in "$@"; do printf '%s\n' "$a"; done; echo "--END--"; } >> "$FAKE_SBX_LOG"
if [ -n "$FAKE_SBX_FAIL" ]; then echo "profile rejected" >&2; exit 1; fi
exit 0
`

func TestSeatbeltCheck(t *testing.T) {
	emptyPath(t)
	if err := NewSeatbelt(SeatbeltOptions{GOOS: "linux"}).Check(); err == nil || !strings.Contains(err.Error(), "macOS") {
		t.Fatalf("other system: %v", err)
	}
	if err := NewSeatbelt(SeatbeltOptions{GOOS: "darwin"}).Check(); err == nil || !strings.Contains(err.Error(), "sandbox-exec") {
		t.Fatalf("missing program: %v", err)
	}
	putFake(t, "sandbox-exec", "exit 0\n")
	if err := NewSeatbelt(SeatbeltOptions{GOOS: "darwin"}).Check(); err != nil {
		t.Fatal(err)
	}
}

func TestSeatbeltPrepareValidatesTheProfileOnce(t *testing.T) {
	putFake(t, "sandbox-exec", fakeSandboxExec)
	log := filepath.Join(t.TempDir(), "sbx.log")
	t.Setenv("FAKE_SBX_LOG", log)
	sb := NewSeatbelt(SeatbeltOptions{GOOS: "darwin", Home: "/Users/dev"})
	s := testSpec(t, RoleBuilder)
	inst, err := sb.Prepare(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if inst.Backend != "seatbelt" || inst.ID == "" || inst.Group != "g1" {
		t.Fatalf("instance %+v", inst)
	}
	raw, _ := os.ReadFile(log)
	if !strings.HasPrefix(string(raw), "-p\n(version 1)\n(deny default)") || !strings.Contains(string(raw), "/usr/bin/true") {
		t.Fatalf("validation call %q", raw)
	}
	t.Setenv("FAKE_SBX_FAIL", "1")
	if _, err := sb.Prepare(context.Background(), s); err == nil || !strings.Contains(err.Error(), "profile rejected") {
		t.Fatalf("a rejected profile must fail Prepare: %v", err)
	}
}

func TestSeatbeltCommandWrapsTheArgv(t *testing.T) {
	putFake(t, "sandbox-exec", fakeSandboxExec)
	t.Setenv("FAKE_SBX_LOG", filepath.Join(t.TempDir(), "sbx.log"))
	sb := NewSeatbelt(SeatbeltOptions{GOOS: "darwin", Home: "/Users/dev"})
	s := testSpec(t, RoleBuilder)
	inst, _ := sb.Prepare(context.Background(), s)
	cmd, err := sb.Command(context.Background(), inst, s, Command{Argv: []string{"go", "build", "-buildvcs=false", "./..."}, Env: []string{"GOCACHE=" + s.Writable[1], "TOKEN=x"}})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(cmd.Path) != "sandbox-exec" || cmd.Args[1] != "-p" || !strings.HasPrefix(cmd.Args[2], "(version 1)") {
		t.Fatalf("args %.80v", cmd.Args)
	}
	if got := strings.Join(cmd.Args[3:], " "); got != "go build ./..." {
		t.Fatalf("wrapped argv %q", got)
	}
	if cmd.Dir != s.Worktree || !contains(cmd.Env, "HOME="+s.Writable[0]) || contains(cmd.Env, "TOKEN=x") {
		t.Fatalf("dir %q env %v", cmd.Dir, cmd.Env)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatal("the command must lead its own process group so a kill takes the whole tree")
	}
}

func TestSeatbeltProfileFollowsTheNetworkSwitch(t *testing.T) {
	putFake(t, "sandbox-exec", fakeSandboxExec)
	t.Setenv("FAKE_SBX_LOG", filepath.Join(t.TempDir(), "sbx.log"))
	sb := NewSeatbelt(SeatbeltOptions{GOOS: "darwin", Home: "/Users/dev"})
	s := testSpec(t, RoleBuilder)
	s.Network = true
	inst, _ := sb.Prepare(context.Background(), s)
	on, _ := sb.Command(context.Background(), inst, s, Command{Argv: []string{"true"}})
	s.Network = false
	off, _ := sb.Command(context.Background(), inst, s, Command{Argv: []string{"true"}})
	if !strings.Contains(on.Args[2], "(allow network*)\n") || strings.Contains(off.Args[2], "(allow network*)\n") {
		t.Fatal("the profile must follow Spec.Network")
	}
}

func TestSeatbeltCaveatsNameTheMissingCaps(t *testing.T) {
	sb := NewSeatbelt(SeatbeltOptions{})
	if got := sb.Caveats(fakeSpec()); len(got) != 2 {
		t.Fatalf("both caps are missing on macOS: %v", got)
	}
	s := fakeSpec()
	s.MaxProcs, s.MaxMemGB = 0, 0
	if got := sb.Caveats(s); len(got) != 0 {
		t.Fatalf("no cap asked, none to report: %v", got)
	}
}

// liveSeatbelt runs real commands under sandbox-exec; it is opt-in because it needs the real program.
func TestSeatbeltLive(t *testing.T) {
	if os.Getenv("KOMODO_TEST_SANDBOX_EXEC") == "" {
		t.Skip("set KOMODO_TEST_SANDBOX_EXEC=1 to run real sandbox-exec commands")
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec is not installed")
	}
	s := testSpec(t, RoleBuilder)
	root := filepath.Dir(s.Worktree)
	secret := filepath.Join(root, "secret.txt")
	masked := filepath.Join(s.Worktree, ".env")
	for p, body := range map[string]string{secret: "s3cret", masked: "K=v", filepath.Join(s.Worktree, "ok.txt"): "fine"} {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(s.Worktree, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(s.Worktree, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.Masked = []string{masked}
	s.Toolchains = nil
	if goBin, err := exec.LookPath("go"); err == nil {
		s.Toolchains = []string{filepath.Dir(goBin)}
	}
	sb :=NewSeatbelt(SeatbeltOptions{Home: root})
	m := NewManager(map[string]Backend{RoleBuilder: sb}, nil)
	inst, err := m.CreateSandboxInstance(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.DestroySandboxInstance(context.Background(), inst.ID) })

	run := func(script string) (string, error) {
		cmd, err := m.Command(context.Background(), inst.ID, Command{Argv: []string{"/bin/sh", "-c", script}, Timeout: 20 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	loop := listenPort(t, "127.0.0.1")
	cases := []struct {
		name   string
		script string
		ok     bool
	}{
		{"write the worktree", "echo hi > ok2.txt && cat ok.txt", true},
		{"write the temp folder", "echo hi > $TMPDIR/x", true},
		{"write the build cache", "echo hi > " + filepath.Join(s.Writable[1], "x"), true},
		{"write outside the worktree", "echo hi > " + outside, false},
		{"rewrite the git pointer", "echo gitdir: /evil > .git/HEAD", false},
		{"create inside the git folder", "touch .git/new", false},
		{"read a closed home file", "cat " + secret, false},
		{"read a masked file", "cat .env", false},
		{"write the module cache", "echo hi > " + filepath.Join(s.Modules, "x"), false},
		{"read a system file", "cat /etc/hosts", true},
		{"connect to loopback", "/usr/bin/nc -z -w 2 127.0.0.1 " + loop, true},
		{"connect past loopback", "/usr/bin/nc -zv -w 2 192.0.2.1 80 2>&1 | grep -q 'not permitted'", true},
		{"only the allowed variables", `test -z "$SECRET_TOKEN" && test "$HOME" = "$TMPDIR"`, true},
	}
	t.Setenv("SECRET_TOKEN", "x")
	for _, tc := range cases {
		out, err := run(tc.script)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err=%v output=%q", tc.name, err, out)
		}
	}
	if len(s.Toolchains) > 0 {
		writeTree(t, s.Worktree, map[string]string{"go.mod": "module live\n\ngo 1.21\n", "main.go": "package main\n\nfunc main() {}\n"})
		cmd, err := m.Command(context.Background(), inst.ID, Command{Argv: []string{"go", "build", "-buildvcs=false", "./..."}, Env: []string{"GOCACHE=" + s.Writable[1]}, Timeout: 120 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("a go build that writes GOCACHE must pass: %v %s", err, out)
		}
	}
	start := time.Now()
	cmd, err := m.Command(context.Background(), inst.ID, Command{Argv: []string{"/bin/sh", "-c", "sleep 30 & sleep 30"}, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Run(); err == nil || time.Since(start) > 10*time.Second {
		t.Errorf("a command past its timeout must die with its tree: err=%v after %s", err, time.Since(start))
	}
	if _, err := os.Stat(outside); err == nil {
		t.Error("the outside file exists")
	}
	if b, _ := os.ReadFile(filepath.Join(s.Worktree, ".git", "HEAD")); strings.Contains(string(b), "evil") {
		t.Error("the git entry was rewritten")
	}
}

// listenPort opens a listener on the address for the rest of the test and returns its port.
func listenPort(t *testing.T, addr string) string {
	t.Helper()
	l, err := net.Listen("tcp", addr+":0")
	if err != nil {
		t.Skipf("cannot listen on %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	return port
}
