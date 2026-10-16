package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// DockerOptions set the docker program and the Tester's image; zero values use docker on PATH and an image detected from the repository.
type DockerOptions struct {
	Binary string // docker
	Image  string // sandbox.image
}

// Docker runs the Tester's commands in one container per group.
type Docker struct {
	opt   DockerOptions
	mu    sync.Mutex
	state map[string]*containerState
}

// containerState is what Docker remembers about one container between calls.
type containerState struct {
	online bool              // true while the bridge network is connected
	masked map[string]fileID // masked files covered by a mount, with the file each mount hides
}

// workDir is where the worktree is mounted inside the container.
const workDir = "/work"

// NewDocker returns the Docker backend.
func NewDocker(o DockerOptions) *Docker {
	if o.Binary == "" {
		o.Binary = "docker"
	}
	return &Docker{opt: o, state: map[string]*containerState{}}
}

// Name returns the mechanism name.
func (d *Docker) Name() string { return "docker" }

// Check reports whether docker is installed and its daemon answers.
func (d *Docker) Check() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := exec.LookPath(d.opt.Binary); err != nil {
		return errors.New("docker is not installed: install Docker, or the Tester runs in the operating-system sandbox")
	}
	if _, err := d.run(ctx, "version", "--format", "{{.Server.Version}}"); err != nil {
		return fmt.Errorf("the docker daemon does not answer: start Docker (%w)", err)
	}
	return nil
}

// Prepare creates and starts the group's container: network on only when the Spec asks for it, and the language tools added.
func (d *Docker) Prepare(ctx context.Context, s Spec) (Instance, error) {
	r, err := newRules(s)
	if err != nil {
		return Instance{}, err
	}
	image, lang, err := d.image(r.Worktree)
	if err != nil {
		return Instance{}, err
	}
	adds := toolAdditions(lang)
	if len(adds) > 0 && !s.Network {
		return Instance{}, fmt.Errorf("the %s tools need the network when the container is made: create the instance with Network on", lang)
	}
	inst, err := newInstance(s.Group, d.Name())
	if err != nil {
		return Instance{}, err
	}
	name := containerName(s.Group, inst.ID)
	args, covered := d.createArgs(r.existing(), inst, name, image)
	if _, err := d.run(ctx, args...); err != nil {
		return Instance{}, fmt.Errorf("create the container: %w", err)
	}
	fail := func(what string, err error) (Instance, error) {
		_, _ = d.run(context.WithoutCancel(ctx), "rm", "-f", name)
		return Instance{}, fmt.Errorf("%s: %w", what, err)
	}
	if _, err := d.run(ctx, "start", name); err != nil {
		return fail("start the container", err)
	}
	for _, add := range adds {
		if _, err := d.run(ctx, append([]string{"exec", "-u", "0", name}, add...)...); err != nil {
			return fail("add the language tools", err)
		}
	}
	d.mu.Lock()
	d.state[inst.ID] = &containerState{online: s.Network, masked: covered}
	d.mu.Unlock()
	return inst, nil
}

// createArgs returns the docker create arguments and the masked files they cover, each with its identity on the host.
func (d *Docker) createArgs(r rules, inst Instance, name, image string) ([]string, map[string]fileID) {
	a := []string{"create", "--name", name, "--init",
		"--label", "komodo.sandbox=1", "--label", "komodo.group=" + inst.Group, "--label", "komodo.instance=" + inst.ID,
		"--workdir", workDir, "--security-opt", "no-new-privileges",
		"--user", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())}
	if r.Network {
		a = append(a, "--network", "bridge")
	} else {
		a = append(a, "--network", "none")
	}
	if r.MaxProcs > 0 {
		a = append(a, "--pids-limit", strconv.Itoa(r.MaxProcs))
	}
	if r.MaxMemGB > 0 {
		a = append(a, "--memory", strconv.Itoa(r.MaxMemGB)+"g")
	}
	a = append(a, "-v", r.Worktree+":"+workDir)
	for _, p := range r.ReadOnly {
		a = append(a, "-v", p+":"+inContainer(r.Worktree, p)+":ro")
	}
	covered := map[string]fileID{}
	for _, p := range r.Masked {
		a = append(a, "-v", "/dev/null:"+inContainer(r.Worktree, p)+":ro")
		if id, ok := identify(p); ok {
			covered[p] = id
		}
	}
	for _, p := range r.Readable {
		if !slices.Contains(r.Toolchains, p) {
			a = append(a, "-v", p+":"+p+":ro")
		}
	}
	for _, p := range r.Writable {
		a = append(a, "-v", p+":"+p)
	}
	return append(a, image, "tail", "-f", "/dev/null"), covered
}

// inContainer maps a host path to where the container sees it: under the worktree it moves to /work, elsewhere it keeps its path.
func inContainer(worktree, p string) string {
	if rel, err := filepath.Rel(worktree, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(filepath.Join(workDir, rel))
	}
	return p
}

// Command wraps the argv in docker exec with the allowed environment; a timeout runs the command under timeout, which kills its process group.
func (d *Docker) Command(ctx context.Context, i Instance, s Spec, c Command) (*exec.Cmd, error) {
	r, err := newRules(s)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	st, ok := d.state[i.ID]
	d.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("container of instance %s was not prepared", i.ID)
	}
	if err := d.guard(st, r); err != nil {
		return nil, err
	}
	bin, err := exec.LookPath(d.opt.Binary)
	if err != nil {
		return nil, errors.New("docker is not installed: install Docker")
	}
	marker := newMarker()
	name := containerName(i.Group, i.ID)
	args := []string{"exec", "-i", "-w", workDir}
	for _, kv := range append(buildEnv(r, c, true), marker) {
		args = append(args, "-e", kv)
	}
	args = append(args, name)
	if c.Timeout > 0 {
		secs := int((c.Timeout + time.Second - 1) / time.Second)
		args = append(args, "timeout", "-k", "5", strconv.Itoa(secs))
	}
	return confined(ctx, launch{
		Timeout: c.Timeout, Bin: bin, Args: append(args, c.Argv...), Marker: marker,
		Remote: func() { d.killMarked(name, marker) },
	}), nil
}

// guard refuses a command whose network state or masked files the container cannot honour.
func (d *Docker) guard(st *containerState, r rules) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case r.Network && !st.online:
		return errors.New("the container was made without a network, so it cannot run the setup command online")
	case !r.Network && st.online:
		return errors.New("the container's network is still connected: end setup first")
	}
	for _, m := range r.Masked {
		now, exists := identify(m)
		if !exists {
			continue
		}
		covered, ok := st.masked[m]
		switch {
		case !ok:
			return fmt.Errorf("masked file %s appeared after the container was made and cannot be hidden from it", m)
		case covered != now:
			return fmt.Errorf("masked file %s was replaced after the container was made, so the container no longer hides it", m)
		}
	}
	return nil
}

// fileID identifies a file on the host by device and inode.
type fileID struct{ dev, ino uint64 }

// identify returns the identity of the file at a path, or false when it is not there.
func identify(path string) (fileID, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return fileID{}, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fileID{}, false
	}
	return fileID{dev: uint64(st.Dev), ino: uint64(st.Ino)}, true
}

// killScript kills every process of the container that carries the marker.
const killScript = `for p in /proc/[0-9]*; do if tr '\0' '\n' < "$p/environ" 2>/dev/null | grep -qx '%s'; then kill -9 "${p#/proc/}" 2>/dev/null; fi; done`

// killMarked kills inside the container every process that carries the marker, because ending the docker client leaves them running.
func (d *Docker) killMarked(name, marker string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = d.run(ctx, "exec", name, "sh", "-c", fmt.Sprintf(killScript, marker))
}

// CutNetwork disconnects the container from the bridge network once setup ends; loopback stays.
func (d *Docker) CutNetwork(ctx context.Context, i Instance) error {
	d.mu.Lock()
	st, ok := d.state[i.ID]
	d.mu.Unlock()
	if !ok || !st.online {
		return nil
	}
	if _, err := d.run(ctx, "network", "disconnect", "bridge", containerName(i.Group, i.ID)); err != nil {
		return err
	}
	d.mu.Lock()
	st.online = false
	d.mu.Unlock()
	return nil
}

// Release removes the container.
func (d *Docker) Release(ctx context.Context, i Instance) error {
	d.mu.Lock()
	delete(d.state, i.ID)
	d.mu.Unlock()
	_, err := d.run(ctx, "rm", "-f", containerName(i.Group, i.ID))
	return err
}

// Caveats is empty: a container enforces both limits.
func (d *Docker) Caveats(Spec) []string { return nil }

// Sweep removes every Komodo container whose group the keep function does not claim, and returns how many it removed.
func (d *Docker) Sweep(ctx context.Context, keep func(group string) bool) (int, error) {
	out, err := d.run(ctx, "ps", "-a", "--filter", "label=komodo.sandbox=1", "--format", `{{.Names}}	{{.Label "komodo.group"}}`)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, line := range strings.Split(out, "\n") {
		name, group, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok || name == "" || keep(group) {
			continue
		}
		if _, err := d.run(ctx, "rm", "-f", name); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// run starts the docker program and returns its output; an error carries the first line it printed to stderr.
func (d *Docker) run(ctx context.Context, args ...string) (string, error) {
	bin, err := exec.LookPath(d.opt.Binary)
	if err != nil {
		return "", errors.New("docker is not installed: install Docker")
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), errors.New(firstLine(stderr.String(), err))
	}
	return stdout.String(), nil
}

// image returns the container image and the language whose tools it needs added; a configured image is used as it is.
func (d *Docker) image(worktree string) (image, lang string, err error) {
	if d.opt.Image == "" {
		return detectImage(worktree)
	}
	return d.opt.Image, "", nil
}

// toolAdditions returns the commands that add the tools the official image of a language lacks.
func toolAdditions(lang string) [][]string {
	switch lang {
	case "rust":
		return [][]string{{"rustup", "component", "add", "clippy", "rustfmt"}}
	case "python":
		return [][]string{{"pip", "install", "uv"}}
	}
	return nil
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// containerName returns the container's name for a group and instance.
func containerName(group, id string) string {
	return "komodo-" + unsafeName.ReplaceAllString(group, "_") + "-" + id[:min(8, len(id))]
}

var (
	goVersion     = regexp.MustCompile(`(?m)^go\s+(\d+\.\d+)`)
	leadingNumber = regexp.MustCompile(`\d+(\.\d+)*`)
	channelLine   = regexp.MustCompile(`(?m)^\s*channel\s*=\s*"([^"]+)"`)
	rustVersion   = regexp.MustCompile(`(?m)^\s*rust-version\s*=\s*"([^"]+)"`)
	pythonReq     = regexp.MustCompile(`(?m)^\s*requires-python\s*=\s*"([^"]+)"`)
)

// detectImage picks the official image from the repository's version files; a repository it cannot decide for needs sandbox.image.
func detectImage(dir string) (image, lang string, err error) {
	langs := codeLanguages(dir)
	if len(langs) != 1 {
		return "", "", fmt.Errorf("the repository has %d languages, so no single image fits: set sandbox.image", len(langs))
	}
	lang = langs[0]
	var tag, repo string
	switch lang {
	case "go":
		repo, tag = "golang", firstMatch(goVersion, readText(dir, "go.mod"))
	case "node":
		repo, tag = "node", nodeVersion(dir)
	case "rust":
		repo, tag = "rust", rustTag(dir)
	case "python":
		repo, tag = "python", pythonTag(dir)
	}
	if tag == "" {
		return "", "", fmt.Errorf("no %s version file names a version, so no image fits: set sandbox.image", lang)
	}
	return repo + ":" + tag, lang, nil
}

// readText returns a file of the folder, or an empty string.
func readText(dir, name string) string {
	b, _ := os.ReadFile(filepath.Join(dir, name))
	return string(b)
}

// firstMatch returns the first group of the first match, or an empty string.
func firstMatch(re *regexp.Regexp, text string) string {
	if m := re.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

// versionIn returns the first dotted number in a version requirement such as ">=18.17".
func versionIn(s string) string { return leadingNumber.FindString(s) }

// nodeVersion reads .nvmrc, then .node-version, then the engines field of package.json.
func nodeVersion(dir string) string {
	for _, f := range []string{".nvmrc", ".node-version"} {
		if v := versionIn(strings.TrimSpace(readText(dir, f))); v != "" {
			return v
		}
	}
	var pkg struct {
		Engines map[string]string `json:"engines"`
	}
	if json.Unmarshal([]byte(readText(dir, "package.json")), &pkg) == nil {
		return versionIn(pkg.Engines["node"])
	}
	return ""
}

// rustTag reads a numeric channel from the toolchain file, then rust-version from Cargo.toml; stable and nightly name no tag.
func rustTag(dir string) string {
	for _, f := range []string{"rust-toolchain.toml", "rust-toolchain"} {
		text := readText(dir, f)
		ch := firstMatch(channelLine, text)
		if ch == "" && !strings.Contains(text, "=") {
			ch = strings.TrimSpace(text)
		}
		if len(ch) > 0 && ch[0] >= '0' && ch[0] <= '9' {
			return versionIn(ch)
		}
	}
	return versionIn(firstMatch(rustVersion, readText(dir, "Cargo.toml")))
}

// pythonTag reads .python-version, then requires-python from pyproject.toml.
func pythonTag(dir string) string {
	if v := versionIn(strings.TrimSpace(readText(dir, ".python-version"))); v != "" {
		return v
	}
	return versionIn(firstMatch(pythonReq, readText(dir, "pyproject.toml")))
}

// List returns the group of every komodo container, running or stopped.
func (d *Docker) List(ctx context.Context) ([]string, error) {
	out, err := d.run(ctx, "ps", "-a", "--filter", "name=^komodo-", "--format", "{{.Names}}")
	if err != nil {
		return nil, err
	}
	var groups []string
	for _, n := range strings.Fields(out) {
		g := strings.TrimPrefix(n, "komodo-")
		if i := strings.LastIndex(g, "-"); i > 0 {
			g = g[:i]
		}
		if !slices.Contains(groups, g) {
			groups = append(groups, g)
		}
	}
	return groups, nil
}

// Remove force-removes every container of the group.
func (d *Docker) Remove(ctx context.Context, group string) error {
	out, err := d.run(ctx, "ps", "-a", "--filter", "name=^komodo-"+unsafeName.ReplaceAllString(group, "_")+"-", "--format", "{{.Names}}")
	if err != nil {
		return err
	}
	for _, n := range strings.Fields(out) {
		if i := strings.LastIndex(n, "-"); i < 0 || n[:i] != "komodo-"+unsafeName.ReplaceAllString(group, "_") {
			continue
		}
		if _, err := d.run(ctx, "rm", "-f", n); err != nil {
			return err
		}
	}
	return nil
}
