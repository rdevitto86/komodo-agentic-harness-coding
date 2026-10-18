package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeDockerScript logs each call as one argument per line closed by --END--, and answers by environment switches.
const fakeDockerScript = `
{ for a in "$@"; do printf '%s\n' "$a"; done; echo "--END--"; } >> "$FAKE_DOCKER_LOG"
case "$1" in
  version)
    if [ -n "$FAKE_DOCKER_DOWN" ]; then
      echo "Cannot connect to the Docker daemon at unix:///var/run/docker.sock" >&2
      exit 1
    fi
    echo 29.0.0 ;;
  create) if [ -n "$FAKE_DOCKER_CREATE_FAIL" ]; then echo "no such image" >&2; exit 1; fi; echo abc123 ;;
  start) if [ -n "$FAKE_DOCKER_START_FAIL" ]; then echo "cannot start" >&2; exit 1; fi ;;
  ps) printf '%s\n' "$FAKE_DOCKER_PS" ;;
  exec) if [ -n "$FAKE_DOCKER_EXEC_SLEEP" ] && [ "$2" = "-i" ]; then exec sleep 30; fi ;;
esac
exit 0
`

// dockerCalls is the logged calls of a fake docker, one argument list each.
type dockerCalls [][]string

// verb returns the calls whose first argument is verb.
func (c dockerCalls) verb(v string) dockerCalls {
	var out dockerCalls
	for _, call := range c {
		if len(call) > 0 && call[0] == v {
			out = append(out, call)
		}
	}
	return out
}

func TestDockerCheckNamesTheFix(t *testing.T) {
	emptyPath(t)
	err := NewDocker(DockerOptions{}).Check()
	if err == nil || !strings.Contains(err.Error(), "docker is not installed") {
		t.Fatalf("missing binary: %v", err)
	}
	calls := fakeDocker(t)
	t.Setenv("FAKE_DOCKER_DOWN", "1")
	err = NewDocker(DockerOptions{}).Check()
	if err == nil || !strings.Contains(err.Error(), "start Docker") || !strings.Contains(err.Error(), "Cannot connect") {
		t.Fatalf("daemon down: %v", err)
	}
	t.Setenv("FAKE_DOCKER_DOWN", "")
	if err := NewDocker(DockerOptions{}).Check(); err != nil {
		t.Fatalf("working docker: %v", err)
	}
	if len(calls().verb("version")) != 2 {
		t.Fatalf("calls %v", calls())
	}
}

func TestDockerPrepareCreatesOneLabelledContainer(t *testing.T) {
	calls := fakeDocker(t)
	s := testSpec(t, RoleTester)
	goRepo(t, s)
	s.Masked = []string{filepath.Join(s.Worktree, ".env")}
	if err := os.WriteFile(s.Masked[0], []byte("K=v"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewManager(map[string]Backend{RoleTester: NewDocker(DockerOptions{})}, nil)
	inst, err := m.CreateSandboxInstance(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if inst.Backend != "docker" || inst.ID == "" || inst.Group != "g1" {
		t.Fatalf("instance %+v", inst)
	}
	create := calls().verb("create")
	if len(create) != 1 {
		t.Fatalf("create calls: %v", calls())
	}
	a := create[0]
	name := "komodo-g1-" + inst.ID[:8]
	wt := s.Worktree
	for _, seq := range [][]string{
		{"--name", name},
		{"--label", "komodo.sandbox=1"}, {"--label", "komodo.group=g1"}, {"--label", "komodo.instance=" + inst.ID},
		{"--network", "none"}, {"--pids-limit", "512"}, {"--memory", "8g"},
		{"--workdir", "/work"}, {"--init"},
		{"--security-opt", "no-new-privileges"},
		{"-v", wt + ":/work"},
		{"-v", filepath.Join(wt, ".git") + ":/work/.git:ro"},
		{"-v", "/dev/null:/work/.env:ro"},
		{"-v", s.GitDir + ":" + s.GitDir + ":ro"},
		{"-v", s.Modules + ":" + s.Modules + ":ro"},
		{"-v", s.Writable[0] + ":" + s.Writable[0]},
		{"-v", s.Writable[1] + ":" + s.Writable[1]},
	} {
		if indexOf(a, seq...) < 0 {
			t.Errorf("create lacks %v:\n%v", seq, a)
		}
	}
	if contains(a, s.Toolchains[0]+":"+s.Toolchains[0]+":ro") {
		t.Error("toolchain folders belong to the host and must not be mounted")
	}
	if n := len(a); a[n-4] != "golang:1.27" || a[n-3] != "tail" || a[n-2] != "-f" || a[n-1] != "/dev/null" {
		t.Fatalf("image and keep-alive: %v", a[n-4:])
	}
	if len(calls().verb("start")) != 1 {
		t.Fatal("the container must be started once")
	}
}

func TestDockerPrepareWithNetworkUsesBridgeAndCutsItAfterSetup(t *testing.T) {
	calls := fakeDocker(t)
	s := testSpec(t, RoleTester)
	goRepo(t, s)
	s.Network = true
	m := NewManager(map[string]Backend{RoleTester: NewDocker(DockerOptions{})}, nil)
	inst, err := m.CreateSandboxInstance(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if indexOf(calls().verb("create")[0], "--network", "bridge") < 0 {
		t.Fatalf("setup needs the bridge: %v", calls())
	}
	if _, err := m.Command(context.Background(), inst.ID, Command{Argv: []string{"go", "mod", "download"}}); err != nil {
		t.Fatal(err)
	}
	if err := m.EndSetup(context.Background(), inst.ID); err != nil {
		t.Fatal(err)
	}
	dc := calls().verb("network")
	if len(dc) != 1 || strings.Join(dc[0], " ") != "network disconnect bridge komodo-g1-"+inst.ID[:8] {
		t.Fatalf("disconnect calls: %v", dc)
	}
	if _, err := m.Command(context.Background(), inst.ID, Command{Argv: []string{"go", "test"}}); err != nil {
		t.Fatalf("after the cut: %v", err)
	}
}

func TestDockerRefusesANetworkCommandInAnOfflineContainer(t *testing.T) {
	fakeDocker(t)
	d := NewDocker(DockerOptions{})
	s := testSpec(t, RoleTester)
	goRepo(t, s)
	inst, err := d.Prepare(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	s.Network = true
	if _, err := d.Command(context.Background(), inst, s, Command{Argv: []string{"true"}}); err == nil {
		t.Fatal("a container made offline cannot run a network command")
	}
}

func TestDockerRefusesACommandWhileTheNetworkIsStillConnected(t *testing.T) {
	fakeDocker(t)
	d := NewDocker(DockerOptions{})
	s := testSpec(t, RoleTester)
	goRepo(t, s)
	s.Network = true
	inst, err := d.Prepare(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	s.Network = false
	if _, err := d.Command(context.Background(), inst, s, Command{Argv: []string{"true"}}); err == nil {
		t.Fatal("a networked container must not run an offline command before its network is cut")
	}
}

func TestDockerCommandIsADockerExecWithTheCleanEnvironment(t *testing.T) {
	fakeDocker(t)
	d := NewDocker(DockerOptions{})
	s := testSpec(t, RoleTester)
	goRepo(t, s)
	inst, err := d.Prepare(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := d.Command(context.Background(), inst, s, Command{
		Argv:    []string{"go", "test", "./..."},
		Env:     []string{"GOCACHE=" + s.Writable[1], "TOKEN=secret"},
		Timeout: 90 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	a := cmd.Args[1:]
	name := "komodo-g1-" + inst.ID[:8]
	if a[0] != "exec" || indexOf(a, "-w", "/work") < 0 || indexOf(a, "-e", "HOME="+s.Writable[0]) < 0 ||
		indexOf(a, "-e", "GOCACHE="+s.Writable[1]) < 0 {
		t.Fatalf("exec args %v", a)
	}
	if strings.Contains(strings.Join(a, " "), "TOKEN") || strings.Contains(strings.Join(a, " "), "PATH=") {
		t.Fatalf("unexpected variable in %v", a)
	}
	tail := a[indexOf(a, name):]
	if strings.Join(tail, " ") != name+" timeout -k 5 90 go test ./..." {
		t.Fatalf("command tail %v", tail)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatal("the docker client must lead its own process group")
	}
	// No timeout means no timeout wrapper.
	cmd, _ = d.Command(context.Background(), inst, s, Command{Argv: []string{"true"}})
	if contains(cmd.Args, "timeout") {
		t.Fatalf("unexpected wrapper: %v", cmd.Args)
	}
}

func TestDockerRefusesAMaskedFileItCouldNotCover(t *testing.T) {
	fakeDocker(t)
	d := NewDocker(DockerOptions{})
	s := testSpec(t, RoleTester)
	goRepo(t, s)
	late := filepath.Join(s.Worktree, ".env.local")
	s.Masked = []string{late}
	inst, err := d.Prepare(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Command(context.Background(), inst, s, Command{Argv: []string{"true"}}); err != nil {
		t.Fatalf("a masked file that does not exist has nothing to hide: %v", err)
	}
	if err := os.WriteFile(late, []byte("K=v"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Command(context.Background(), inst, s, Command{Argv: []string{"true"}}); err == nil ||
		!strings.Contains(err.Error(), ".env.local") {
		t.Fatalf("a mask that appeared after the container was made must stop the command: %v", err)
	}
}

func TestDockerKeepsAcceptingTheMasksItCovered(t *testing.T) {
	fakeDocker(t)
	d := NewDocker(DockerOptions{})
	s := testSpec(t, RoleTester)
	goRepo(t, s)
	env := filepath.Join(s.Worktree, ".env")
	if err := os.WriteFile(env, []byte("K=v"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Masked = []string{env, filepath.Join(s.Worktree, "never-created.pem")}
	inst, err := d.Prepare(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := d.Command(context.Background(), inst, s, Command{Argv: []string{"true"}}); err != nil {
			t.Fatalf("command %d: %v", i, err)
		}
	}
}

func TestDockerRefusesAMaskedFileThatWasReplacedOnTheHost(t *testing.T) {
	fakeDocker(t)
	d := NewDocker(DockerOptions{})
	s := testSpec(t, RoleTester)
	goRepo(t, s)
	env := filepath.Join(s.Worktree, ".env")
	if err := os.WriteFile(env, []byte("K=v"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Masked = []string{env}
	inst, err := d.Prepare(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(s.Worktree, ".env.tmp")
	if err := os.WriteFile(tmp, []byte("K=other"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, env); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Command(context.Background(), inst, s, Command{Argv: []string{"true"}}); err == nil ||
		!strings.Contains(err.Error(), "replaced") {
		t.Fatalf("a replaced masked file is no longer covered by the mount: %v", err)
	}
}

func TestDockerCustomImageGetsNoToolAdditions(t *testing.T) {
	calls := fakeDocker(t)
	s := testSpec(t, RoleTester)
	if err := os.WriteFile(filepath.Join(s.Worktree, "Cargo.toml"), []byte("rust-version = \"1.78\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDocker(DockerOptions{Image: "my/rust:1"}).Prepare(context.Background(), s); err != nil {
		t.Fatalf("a custom image is used as it is, offline: %v", err)
	}
	if n := len(calls().verb("exec")); n != 0 {
		t.Fatalf("%d tool additions ran in a custom image", n)
	}
}

func TestDockerReleaseRemovesTheContainer(t *testing.T) {
	calls := fakeDocker(t)
	d := NewDocker(DockerOptions{})
	s := testSpec(t, RoleTester)
	goRepo(t, s)
	inst, _ := d.Prepare(context.Background(), s)
	if err := d.Release(context.Background(), inst); err != nil {
		t.Fatal(err)
	}
	rm := calls().verb("rm")
	if len(rm) != 1 || strings.Join(rm[0], " ") != "rm -f komodo-g1-"+inst.ID[:8] {
		t.Fatalf("rm calls %v", rm)
	}
}

func TestDockerPrepareFailureLeavesNoContainer(t *testing.T) {
	calls := fakeDocker(t)
	s := testSpec(t, RoleTester)
	goRepo(t, s)
	t.Setenv("FAKE_DOCKER_START_FAIL", "1")
	if _, err := NewDocker(DockerOptions{}).Prepare(context.Background(), s); err == nil ||
		!strings.Contains(err.Error(), "cannot start") {
		t.Fatalf("got %v", err)
	}
	if len(calls().verb("rm")) != 1 {
		t.Fatalf("a failed start must remove the created container: %v", calls())
	}
	t.Setenv("FAKE_DOCKER_START_FAIL", "")
	t.Setenv("FAKE_DOCKER_CREATE_FAIL", "1")
	if _, err := NewDocker(DockerOptions{}).Prepare(context.Background(), s); err == nil ||
		!strings.Contains(err.Error(), "no such image") {
		t.Fatalf("got %v", err)
	}
}

func TestDockerPrepareNeedsAnImage(t *testing.T) {
	fakeDocker(t)
	s := testSpec(t, RoleTester)
	_, err := NewDocker(DockerOptions{}).Prepare(context.Background(), s)
	if err == nil || !strings.Contains(err.Error(), "sandbox.image") {
		t.Fatalf("a repository with no language needs sandbox.image: %v", err)
	}
	calls := fakeDocker(t)
	inst, err := NewDocker(DockerOptions{Image: "my/image:1"}).Prepare(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	a := calls().verb("create")[0]
	if a[len(a)-4] != "my/image:1" || inst.ID == "" {
		t.Fatalf("configured image ignored: %v", a)
	}
}

func TestDockerAddsRustComponentsAsRoot(t *testing.T) {
	calls := fakeDocker(t)
	s := testSpec(t, RoleTester)
	s.Network = true
	if err := os.WriteFile(
		filepath.Join(s.Worktree, "Cargo.toml"),
		[]byte("[package]\nname=\"x\"\nrust-version = \"1.78\"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	inst, err := NewDocker(DockerOptions{}).Prepare(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	name := "komodo-g1-" + inst.ID[:8]
	exec := calls().verb("exec")
	if len(exec) != 1 || strings.Join(exec[0], " ") != "exec -u 0 "+name+" rustup component add clippy rustfmt" {
		t.Fatalf("exec calls %v", exec)
	}
	if a := calls().verb("create")[0]; a[len(a)-4] != "rust:1.78" {
		t.Fatalf("image %v", a[len(a)-4:])
	}
}

func TestDockerAdditionsNeedTheNetwork(t *testing.T) {
	fakeDocker(t)
	s := testSpec(t, RoleTester)
	if err := os.WriteFile(filepath.Join(s.Worktree, "Cargo.toml"), []byte("rust-version = \"1.78\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDocker(DockerOptions{}).Prepare(context.Background(), s); err == nil ||
		!strings.Contains(err.Error(), "network") {
		t.Fatalf("got %v", err)
	}
}

func TestDockerSweepRemovesContainersOfClosedGroups(t *testing.T) {
	calls := fakeDocker(t)
	t.Setenv("FAKE_DOCKER_PS", "komodo-g1-aaaaaaaa\tg1\nkomodo-g2-bbbbbbbb\tg2")
	n, err := NewDocker(DockerOptions{}).Sweep(context.Background(), func(group string) bool { return group == "g1" })
	if err != nil || n != 1 {
		t.Fatalf("removed %d, err %v", n, err)
	}
	rm := calls().verb("rm")
	if len(rm) != 1 || strings.Join(rm[0], " ") != "rm -f komodo-g2-bbbbbbbb" {
		t.Fatalf("rm calls %v", rm)
	}
	ps := calls().verb("ps")[0]
	if indexOf(ps, "--filter", "label=komodo.sandbox=1") < 0 || !contains(ps, "-a") {
		t.Fatalf("ps args %v", ps)
	}
}

func TestDockerCaveatsNameNothingWhenCapsAreEnforced(t *testing.T) {
	if got := NewDocker(DockerOptions{}).Caveats(fakeSpec()); len(got) != 0 {
		t.Fatalf("docker enforces both caps: %v", got)
	}
}

func TestDetectImage(t *testing.T) {
	write := func(t *testing.T, files map[string]string) string {
		t.Helper()
		dir := t.TempDir()
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	cases := []struct {
		name  string
		files map[string]string
		image string
		lang  string
		fail  string
	}{
		{"go", map[string]string{"go.mod": "module x\n\ngo 1.27.2\ntoolchain go1.27.3\n"}, "golang:1.27", "go", ""},
		{"node nvmrc", map[string]string{"package.json": "{}", ".nvmrc": "v20.11.0\n"}, "node:20.11.0", "node", ""},
		{"node engines", map[string]string{"package.json": `{"engines":{"node":">=18.17"}}`}, "node:18.17", "node", ""},
		{
			"rust toolchain file",
			map[string]string{"Cargo.toml": "", "rust-toolchain.toml": "[toolchain]\nchannel = \"1.78.0\"\n"},
			"rust:1.78.0",
			"rust",
			"",
		},
		{
			"python",
			map[string]string{"pyproject.toml": "[project]\nrequires-python = \">=3.11\"\n"},
			"python:3.11",
			"python",
			"",
		},
		{
			"python version file",
			map[string]string{"pyproject.toml": "", ".python-version": "3.12.1\n"},
			"python:3.12.1",
			"python",
			"",
		},
		{"makefile only", map[string]string{"Makefile": "all:\n"}, "", "", "sandbox.image"},
		{
			"two languages",
			map[string]string{"go.mod": "go 1.27\n", "package.json": "{}", ".nvmrc": "20"},
			"",
			"",
			"sandbox.image",
		},
		{"no version file", map[string]string{"package.json": "{}"}, "", "", "sandbox.image"},
		{
			"stable rust has no tag",
			map[string]string{"Cargo.toml": "", "rust-toolchain.toml": "[toolchain]\nchannel = \"stable\"\n"},
			"",
			"",
			"sandbox.image",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			image, lang, err := detectImage(write(t, tc.files))
			if tc.fail != "" {
				if err == nil || !strings.Contains(err.Error(), tc.fail) {
					t.Fatalf("want error naming %s, got %v", tc.fail, err)
				}
				return
			}
			if err != nil || image != tc.image || lang != tc.lang {
				t.Fatalf("got %q %q %v", image, lang, err)
			}
		})
	}
}

// fakeDocker installs the fake docker and returns a reader of its calls.
func fakeDocker(t *testing.T) func() dockerCalls {
	t.Helper()
	putFake(t, "docker", fakeDockerScript)
	log := filepath.Join(t.TempDir(), "docker.log")
	t.Setenv("FAKE_DOCKER_LOG", log)
	return func() dockerCalls {
		raw, err := os.ReadFile(log)
		if err != nil {
			return nil
		}
		var calls dockerCalls
		var cur []string
		for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
			if line == "--END--" {
				calls = append(calls, cur)
				cur = nil
				continue
			}
			cur = append(cur, line)
		}
		return calls
	}
}

// goRepo writes a go.mod into the worktree so the image can be detected.
func goRepo(t *testing.T, s Spec) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.Worktree, "go.mod"), []byte("module x\n\ngo 1.27.2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
