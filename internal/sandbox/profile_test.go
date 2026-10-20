package sandbox

import (
	"strings"
	"testing"
)

func TestNewRulesSplitsTheSpec(t *testing.T) {
	s := fakeSpec()
	r := mustRules(t, s)
	if r.Worktree != s.Worktree || r.Temp != "/private/tmp/komodo-s1" {
		t.Fatalf("worktree %q temp %q", r.Worktree, r.Temp)
	}
	if !contains(r.ReadOnly, s.Worktree+"/.git") {
		t.Fatalf("ReadOnly %v lacks the git pointer", r.ReadOnly)
	}
	for _, want := range []string{s.GitDir, s.Modules, "/Users/dev/.rustup"} {
		if !contains(r.Readable, want) {
			t.Errorf("Readable lacks %s", want)
		}
	}
	if len(r.Writable) != 2 || r.MaxProcs != 512 || r.MaxMemGB != 8 {
		t.Fatalf("writable %v limits %d %d", r.Writable, r.MaxProcs, r.MaxMemGB)
	}
}

func TestNewRulesKeepsOneGitEntryAndCleansPaths(t *testing.T) {
	s := fakeSpec()
	s.ReadOnly = []string{s.Worktree + "/.git", s.Worktree + "/./.git/"}
	r := mustRules(t, s)
	n := 0
	for _, p := range r.ReadOnly {
		if p == s.Worktree+"/.git" {
			n++
		}
	}
	if n != 1 || len(r.ReadOnly) != 1 {
		t.Fatalf("ReadOnly %v", r.ReadOnly)
	}
}

func TestNewRulesRefusesPathsADockerMountCannotCarry(t *testing.T) {
	s := fakeSpec()
	s.Worktree = "/Users/dev/a:b"
	if _, err := newRules(s); err == nil {
		t.Fatal("a colon in a path must be refused")
	}
	s = fakeSpec()
	s.Modules = "/Users/dev/line\nbreak"
	if _, err := newRules(s); err == nil {
		t.Fatal("a newline in a path must be refused")
	}
}

func TestEnvHoldsOnlyTheAllowedVariables(t *testing.T) {
	r := mustRules(t, fakeSpec())
	env := buildEnv(r, Command{Env: []string{
		"LANG=en_US.UTF-8", "GOCACHE=/c/go", "SECRET_TOKEN=x", "AWS_SECRET_ACCESS_KEY=y",
		"PATH=/evil", "HOME=/evil", "TMPDIR=/evil", "RUSTUP_HOME=/u/rustup", "CARGO_TARGET_DIR=/t",
	}}, false)
	got := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	if got["HOME"] != "/private/tmp/komodo-s1" || got["TMPDIR"] != "/private/tmp/komodo-s1" {
		t.Fatalf("HOME %q TMPDIR %q", got["HOME"], got["TMPDIR"])
	}
	if got["LANG"] != "en_US.UTF-8" || got["GOCACHE"] != "/c/go" || got["RUSTUP_HOME"] != "/u/rustup" ||
		got["CARGO_TARGET_DIR"] != "/t" {
		t.Fatalf("lost an allowed variable: %v", got)
	}
	for _, bad := range []string{"SECRET_TOKEN", "AWS_SECRET_ACCESS_KEY"} {
		if _, ok := got[bad]; ok {
			t.Errorf("%s leaked", bad)
		}
	}
	if !strings.HasPrefix(got["PATH"], "/Users/dev/.rustup:") || strings.Contains(got["PATH"], "/evil") ||
		!strings.HasSuffix(got["PATH"], "/usr/bin:/bin:/usr/sbin:/sbin") {
		t.Fatalf("PATH %q", got["PATH"])
	}
	if len(env) != len(got) {
		t.Fatalf("duplicate variables: %v", env)
	}
}

func TestEnvDefaultsLangAndLeavesPathToTheContainer(t *testing.T) {
	r := mustRules(t, fakeSpec())
	env := buildEnv(r, Command{}, true)
	joined := "\n" + strings.Join(env, "\n") + "\n"
	if !strings.Contains(joined, "\nLANG=C.UTF-8\n") || strings.Contains(joined, "\nPATH=") {
		t.Fatalf("env %v", env)
	}
}

func TestEnvPointsGoAtTheReadOnlyModuleCache(t *testing.T) {
	s := fakeSpec()
	r := mustRules(t, s)
	for _, container := range []bool{false, true} {
		env := buildEnv(r, Command{Env: []string{"GOMODCACHE=/writable/mod"}}, container)
		if !contains(env, "GOMODCACHE="+s.Modules) || contains(env, "GOMODCACHE=/writable/mod") {
			t.Errorf("container %v: env %v lacks GOMODCACHE=%s", container, env, s.Modules)
		}
	}

	s.Modules = ""
	env := buildEnv(mustRules(t, s), Command{Env: []string{"GOMODCACHE=/own/mod"}}, false)
	if !contains(env, "GOMODCACHE=/own/mod") {
		t.Errorf("without a module cache a command keeps its own GOMODCACHE: %v", env)
	}
}

func TestSeatbeltProfileOrdersItsRules(t *testing.T) {
	s := fakeSpec()
	p := seatbeltProfile(mustRules(t, s), "/Users/dev")
	if !strings.HasPrefix(p, "(version 1)\n(deny default)\n") {
		t.Fatalf("profile starts %q", p[:40])
	}
	at := func(sub string) int {
		i := strings.Index(p, sub)
		if i < 0 {
			t.Fatalf("profile lacks %q:\n%s", sub, p)
		}
		return i
	}
	closed := at(`(deny file-read* (subpath "/Users/dev"))`)
	reopened := at(`(allow file-read* (subpath "/Users/dev/.komodo/wt/shop/g1")`)
	masked := at(`(deny file-read* (literal "/Users/dev/.komodo/wt/shop/g1/.env")`)
	writes := at(`(allow file-write* (subpath "/Users/dev/.komodo/wt/shop/g1")`)
	gitDeny := at(`(deny file-write* (subpath "/Users/dev/.komodo/wt/shop/g1/.git"))`)
	if closed >= reopened || reopened >= masked || masked >= writes || writes >= gitDeny {
		t.Fatalf("rule order wrong: %d %d %d %d %d", closed, reopened, masked, writes, gitDeny)
	}
	for _, want := range []string{
		`(subpath "/Users/dev/src/shop/.git")`, `(subpath "/Users/dev/.rustup")`,
		`(subpath "/Users/dev/.komodo/cache/lang/shop/modules")`, `(literal "/Users/dev/.komodo/wt/shop/g1/tls.pem")`,
		`(subpath "/private/tmp/komodo-s1")`, `(subpath "/Users/dev/.komodo/cache/lang/shop/build")`,
		`(subpath "/private/var/run")`,
	} {
		at(want)
	}
	w := p[writes:gitDeny]
	for _, no := range []string{"/modules", ".rustup", "src/shop/.git"} {
		if strings.Contains(w, no) {
			t.Errorf("write allow list holds %s", no)
		}
	}
}

func TestSeatbeltNetworkIsLoopbackOnlyUnlessSetup(t *testing.T) {
	r := mustRules(t, fakeSpec())
	off := seatbeltProfile(r, "/Users/dev")
	if !strings.Contains(off, `(allow network-outbound (remote ip "localhost:*"))`) ||
		!strings.Contains(off, `(allow network-bind network-inbound (local ip "localhost:*"))`) ||
		strings.Contains(off, "(allow network*)") {
		t.Fatalf("network off profile:\n%s", off)
	}
	s := fakeSpec()
	s.Network = true
	on := seatbeltProfile(mustRules(t, s), "/Users/dev")
	if strings.Contains(on, "(allow network*)") || !strings.Contains(on, "(allow network-outbound (remote ip))\n") {
		t.Fatalf("network on profile:\n%s", on)
	}
}

func TestSeatbeltQuotesPaths(t *testing.T) {
	s := fakeSpec()
	s.Masked = []string{`/Users/dev/.komodo/wt/shop/g1/a"b\c`}
	p := seatbeltProfile(mustRules(t, s), "/Users/dev")
	if !strings.Contains(p, `(literal "/Users/dev/.komodo/wt/shop/g1/a\"b\\c")`) {
		t.Fatalf("quotes not escaped:\n%s", p)
	}
}

func TestSeatbeltWithoutMasksOrModules(t *testing.T) {
	s := fakeSpec()
	s.Masked, s.Modules, s.Toolchains = nil, "", nil
	p := seatbeltProfile(mustRules(t, s), "/Users/dev")
	if strings.Contains(p, "literal") || strings.Contains(p, "()") {
		t.Fatalf("empty lists must render no rule:\n%s", p)
	}
}

func TestBwrapArgsFollowTheDocumentedLine(t *testing.T) {
	r := mustRules(t, fakeSpec())
	a := bwrapArgs(r, "/home/dev", true)
	wt := "/Users/dev/.komodo/wt/shop/g1"
	for _, seq := range [][]string{
		{"--ro-bind", "/", "/"},
		{"--tmpfs", "/home/dev"},
		{"--tmpfs", "/tmp"},
		{"--tmpfs", "/run"},
		{"--tmpfs", "/var/run"},
		{"--bind", wt, wt},
		{"--ro-bind", wt + "/.git", wt + "/.git"},
		{"--ro-bind", "/Users/dev/src/shop/.git", "/Users/dev/src/shop/.git"},
		{"--ro-bind", "/Users/dev/.komodo/cache/lang/shop/modules", "/Users/dev/.komodo/cache/lang/shop/modules"},
		{"--ro-bind", "/Users/dev/.rustup", "/Users/dev/.rustup"},
		{"--bind", "/Users/dev/.komodo/cache/lang/shop/build", "/Users/dev/.komodo/cache/lang/shop/build"},
		{"--bind", "/private/tmp/komodo-s1", "/private/tmp/komodo-s1"},
		{"--ro-bind", "/dev/null", wt + "/.env"},
		{"--ro-bind", "/dev/null", wt + "/tls.pem"},
		{"--unshare-net"}, {"--unshare-pid"}, {"--die-with-parent"}, {"--new-session"},
		{"--dev", "/dev"}, {"--proc", "/proc"},
	} {
		if indexOf(a, seq...) < 0 {
			t.Errorf("args lack %v:\n%v", seq, a)
		}
	}
	if n := len(a); a[n-2] != "--chdir" || a[n-1] != wt {
		t.Fatalf("args end %v", a[n-2:])
	}
	if indexOf(a, "--tmpfs", "/tmp") > indexOf(a, "--bind", "/private/tmp/komodo-s1", "/private/tmp/komodo-s1") {
		t.Fatal("/tmp must be a tmpfs before the instance folder is bound back")
	}
	if indexOf(a, "--tmpfs", "/home/dev") > indexOf(a, "--bind", wt, wt) {
		t.Fatal("home must be emptied before the worktree is bound back")
	}
}

func TestBwrapNetworkAndRunFolder(t *testing.T) {
	s := fakeSpec()
	s.Network = true
	a := bwrapArgs(mustRules(t, s), "/home/dev", false)
	if contains(a, "--unshare-net") {
		t.Fatal("the setup command keeps the network")
	}
	if indexOf(a, "--tmpfs", "/var/run") >= 0 {
		t.Fatal("/var/run is a link on this host and must not be mounted over")
	}
	if indexOf(a, "--tmpfs", "/run") < 0 {
		t.Fatal("/run must be emptied")
	}
}

func TestSeatbeltFreezesMaskedFilesAndTheirFolders(t *testing.T) {
	s := fakeSpec()
	wt := s.Worktree
	s.Masked = append(s.Masked, wt+"/cfg/deep/.env")
	p := seatbeltProfile(mustRules(t, s), "/Users/dev")
	i := strings.Index(p, "(deny file-write* (literal ")
	if i < 0 {
		t.Fatalf("no write denial for masked files:\n%s", p)
	}
	rule := p[i : i+strings.Index(p[i:], "))\n")+3]
	for _, m := range s.Masked {
		if !strings.Contains(rule, `(literal "`+m+`")`) {
			t.Errorf("masked %s can still be written, renamed or removed", m)
		}
	}
	j := strings.Index(p, "(deny file-write-unlink (literal ")
	if j < 0 {
		t.Fatalf("no unlink denial for the folders above a masked file:\n%s", p)
	}
	unlink := p[j : j+strings.Index(p[j:], "))\n")+3]
	for _, dir := range []string{wt + "/cfg", wt + "/cfg/deep"} {
		if !strings.Contains(unlink, `(literal "`+dir+`")`) {
			t.Errorf("folder %s can be renamed away with a masked file inside", dir)
		}
	}
	if strings.Contains(unlink, `(literal "`+wt+`")`) {
		t.Error("the worktree itself must not be frozen")
	}
}

func TestSeatbeltScopesSignalsAndMachLookups(t *testing.T) {
	p := seatbeltProfile(mustRules(t, fakeSpec()), "/Users/dev")
	if !strings.Contains(p, "(allow signal (target same-sandbox))\n") {
		t.Fatalf("signals must stay inside the sandbox:\n%s", p)
	}
	for _, line := range strings.Split(p, "\n") {
		if strings.HasPrefix(line, "(allow process-exec") &&
			(strings.Contains(line, "signal") || strings.Contains(line, "mach-lookup")) {
			t.Fatalf("unfiltered operation on %q", line)
		}
	}
	if !strings.Contains(p, `(allow mach-lookup (global-name "com.apple.`) || strings.Contains(p, "mach-lookup)") {
		t.Fatalf("mach lookups need an allow-list:\n%s", p)
	}
}

func TestSeatbeltUnixSocketsStayInsideTheWorktreeAndTemp(t *testing.T) {
	for _, network := range []bool{false, true} {
		s := fakeSpec()
		s.Network = network
		p := seatbeltProfile(mustRules(t, s), "/Users/dev")
		for _, want := range []string{
			`(allow network-outbound (remote unix-socket (subpath "/Users/dev/.komodo/wt/shop/g1")`,
			`(subpath "/private/tmp/komodo-s1")`,
			`(allow network-bind (local unix-socket (subpath "/Users/dev/.komodo/wt/shop/g1")`,
		} {
			if !strings.Contains(p, want) {
				t.Errorf("network=%v lacks %s", network, want)
			}
		}
		if strings.Contains(p, "(allow network*)") {
			t.Errorf("network=%v: a bare network allow opens every Unix socket", network)
		}
	}
}

// fakeSpec is a Spec over paths that do not exist, for pure rendering tests.
func fakeSpec() Spec {
	return Spec{
		Group:      "g1",
		Worktree:   "/Users/dev/.komodo/wt/shop/g1",
		GitDir:     "/Users/dev/src/shop/.git",
		Writable:   []string{"/private/tmp/komodo-s1", "/Users/dev/.komodo/cache/lang/shop/build"},
		Modules:    "/Users/dev/.komodo/cache/lang/shop/modules",
		Toolchains: []string{"/Users/dev/.rustup"},
		Masked:     []string{"/Users/dev/.komodo/wt/shop/g1/.env", "/Users/dev/.komodo/wt/shop/g1/tls.pem"},
		MaxProcs:   512,
		MaxMemGB:   8,
		Role:       RoleBuilder,
	}
}

func mustRules(t *testing.T, s Spec) rules {
	t.Helper()
	r, err := newRules(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
