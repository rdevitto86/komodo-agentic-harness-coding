package sandbox

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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

func TestSeatbeltFailsClosedWithoutAHomeFolder(t *testing.T) {
	putFake(t, "sandbox-exec", fakeSandboxExec)
	t.Setenv("FAKE_SBX_LOG", filepath.Join(t.TempDir(), "sbx.log"))
	t.Setenv("HOME", "")
	sb := NewSeatbelt(SeatbeltOptions{GOOS: "darwin"})
	if err := sb.Check(); err == nil || !strings.Contains(err.Error(), "home") {
		t.Fatalf("Check: %v", err)
	}
	if _, err := sb.Prepare(context.Background(), testSpec(t, RoleBuilder)); err == nil || !strings.Contains(err.Error(), "home") {
		t.Fatalf("Prepare: %v", err)
	}
}

func TestSeatbeltCommandWrapsTheArgv(t *testing.T) {
	putFake(t, "sandbox-exec", fakeSandboxExec)
	t.Setenv("FAKE_SBX_LOG", filepath.Join(t.TempDir(), "sbx.log"))
	sb := NewSeatbelt(SeatbeltOptions{GOOS: "darwin", Home: "/Users/dev"})
	s := testSpec(t, RoleBuilder)
	inst, _ := sb.Prepare(context.Background(), s)
	cmd, err := sb.Command(context.Background(), inst, s, Command{Argv: []string{"go", "build", "./..."}, Env: []string{"GOCACHE=" + s.Writable[1], "TOKEN=x"}})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(cmd.Path) != "sandbox-exec" || cmd.Args[1] != "-p" || !strings.HasPrefix(cmd.Args[2], "(version 1)") {
		t.Fatalf("args %.80v", cmd.Args)
	}
	if got := strings.Join(cmd.Args[3:], " "); !strings.HasSuffix(got, " sh go build ./...") || !strings.Contains(got, "kill -9 -1") {
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
	if !strings.Contains(on.Args[2], "(allow network-outbound (remote ip))\n") || strings.Contains(off.Args[2], "(allow network-outbound (remote ip))\n") {
		t.Fatal("the profile must follow Spec.Network")
	}
}

func TestSeatbeltCaveatsNameTheMissingCaps(t *testing.T) {
	sb := NewSeatbelt(SeatbeltOptions{})
	got := sb.Caveats(fakeSpec())
	if len(got) != 3 || !strings.Contains(strings.Join(got, "\n"), "loopback") {
		t.Fatalf("two caps and the shared loopback must be named: %v", got)
	}
	s := fakeSpec()
	s.MaxProcs, s.MaxMemGB = 0, 0
	if got := sb.Caveats(s); len(got) != 1 || !strings.Contains(got[0], "loopback") {
		t.Fatalf("only the shared loopback remains: %v", got)
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
	nested := filepath.Join(s.Worktree, "cfg", ".env")
	writeTree(t, s.Worktree, map[string]string{"cfg/.env": "N=1"})
	s.Masked = []string{masked, nested}
	s.Toolchains = nil
	if goBin, err := exec.LookPath("go"); err == nil {
		s.Toolchains = []string{filepath.Dir(goBin)}
	}
	sb := NewSeatbelt(SeatbeltOptions{Home: root})
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
	sockDir, err := os.MkdirTemp("/tmp", "ksb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	hostSock := filepath.Join(sockDir, "agent.sock")
	ln, err := net.Listen("unix", hostSock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	if out, err := exec.Command("/bin/sh", "-c", unixConnect(hostSock)).CombinedOutput(); err != nil {
		t.Fatalf("the host socket is not reachable even without a sandbox: %v %s", err, out)
	}
	hostProc := exec.Command("sleep", "60")
	if err := hostProc.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hostProc.Process.Kill(); _ = hostProc.Wait() })
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
		{"rename a masked file away and read it", "mv .env stolen && cat stolen", false},
		{"rewrite a masked file", "echo x > .env", false},
		{"delete a masked file", "rm -f .env && test ! -e .env", false},
		{"rename the folder that holds a masked file", "mv cfg stolen && cat stolen/.env", false},
		{"hard link a masked file and read the link", "ln .env linked && cat linked", false},
		{"signal a host process", "kill -9 " + strconv.Itoa(hostProc.Process.Pid), false},
		{"signal its own child", "sleep 30 & kill -9 $!", true},
		{"connect to a host unix socket", unixConnect(hostSock), false},
		{"serve and use a unix socket in the worktree", `/usr/bin/perl -MIO::Socket::UNIX -e '$s = IO::Socket::UNIX->new(Local => "s.sock", Listen => 1) or die "bind: $!\n"; IO::Socket::UNIX->new(Peer => "s.sock") or die "connect: $!\n"'; r=$?; rm -f s.sock; exit $r`, true},
		{"common tools run", "ls >/dev/null && /usr/bin/awk 'BEGIN{print 1}' >/dev/null && /usr/bin/perl -e 'print 1' >/dev/null && /usr/bin/grep -q . ok.txt && /usr/bin/git --version >/dev/null", true},
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
	if err := hostProc.Process.Signal(syscall.Signal(0)); err != nil {
		t.Errorf("the host process was signalled: %v", err)
	}
	if b, _ := os.ReadFile(masked); string(b) != "K=v" {
		t.Errorf("the masked file changed: %q", b)
	}

	// A descendant that starts its own session must still die with the command's timeout.
	pidFile := filepath.Join(s.Writable[0], "escapee.pid")
	script := `/usr/bin/perl -e 'use POSIX qw(setsid); if (fork) { exit 0 } setsid(); open(F, ">", $ARGV[0]); print F $$; close F; exec("sleep", "30")' ` + pidFile + `; sleep 30`
	cmd, err = m.Command(context.Background(), inst.ID, Command{Argv: []string{"/bin/sh", "-c", script}, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_ = cmd.Run()
	raw, _ := os.ReadFile(pidFile)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	if pid == 0 {
		t.Fatal("the escaping child never wrote its pid")
	}
	time.Sleep(300 * time.Millisecond)
	if err := syscall.Kill(pid, 0); err == nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Error("a descendant in its own session survived the timeout")
	}

	// The setup instance has the network on: Unix sockets outside the worktree stay closed and TCP works.
	online := s
	online.Network = true
	oi, err := m.CreateSandboxInstance(context.Background(), online)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.DestroySandboxInstance(context.Background(), oi.ID) })
	for _, tc := range []struct {
		name, script string
		ok           bool
	}{
		{"online: connect to a host unix socket", unixConnect(hostSock), false},
		{"online: resolve a name", "/usr/bin/nc -z -w 2 localhost " + loop, true},
		{"online: connect to loopback", "/usr/bin/nc -z -w 2 127.0.0.1 " + loop, true},
		{"online: read a masked file", "cat .env", false},
	} {
		cmd, err := m.Command(context.Background(), oi.ID, Command{Argv: []string{"/bin/sh", "-c", tc.script}, Timeout: 20 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		if out, err := cmd.CombinedOutput(); (err == nil) != tc.ok {
			t.Errorf("%s: err=%v output=%q", tc.name, err, out)
		}
	}
	if _, err := os.Stat(outside); err == nil {
		t.Error("the outside file exists")
	}
	if b, _ := os.ReadFile(filepath.Join(s.Worktree, ".git", "HEAD")); strings.Contains(string(b), "evil") {
		t.Error("the git entry was rewritten")
	}
}

// unixConnect returns a shell command that connects to a Unix socket and fails when it cannot.
func unixConnect(path string) string {
	return `/usr/bin/perl -MIO::Socket::UNIX -e 'IO::Socket::UNIX->new(Peer => $ARGV[0]) or die "$!\n"' ` + path
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
