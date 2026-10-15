package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"
)

// BubblewrapOptions override where Bubblewrap finds its program, the person's home folder and the operating system; zero values use the host's.
type BubblewrapOptions struct {
	Binary string // bwrap
	Home   string // replaced by an empty temporary file system
	GOOS   string
}

// Bubblewrap confines a command with Linux namespaces through bwrap.
type Bubblewrap struct{ opt BubblewrapOptions }

// NewBubblewrap returns the Bubblewrap backend.
func NewBubblewrap(o BubblewrapOptions) *Bubblewrap {
	if o.Binary == "" {
		o.Binary = "bwrap"
	}
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.Home == "" {
		o.Home, _ = os.UserHomeDir()
	}
	return &Bubblewrap{opt: o}
}

// Name returns the mechanism name.
func (b *Bubblewrap) Name() string { return "bubblewrap" }

// Check reports whether bwrap can start a sandbox here, which needs user namespaces.
func (b *Bubblewrap) Check() error {
	if b.opt.GOOS != "linux" {
		return errors.New("Bubblewrap runs on Linux and Windows Subsystem for Linux only")
	}
	bin, err := exec.LookPath(b.opt.Binary)
	if err != nil {
		return fmt.Errorf("%s is not installed: install the bubblewrap package", b.opt.Binary)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, "--ro-bind", "/", "/", "--unshare-pid", "true")
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("user namespaces are blocked (%s): enable unprivileged user namespaces, for example sysctl kernel.unprivileged_userns_clone=1", firstLine(stderr.String(), err))
	}
	return nil
}

// Prepare gives the instance an id; the arguments are rendered for each command from the Spec.
func (b *Bubblewrap) Prepare(_ context.Context, s Spec) (Instance, error) {
	if _, err := newRules(s); err != nil {
		return Instance{}, err
	}
	return newInstance(s.Group, b.Name())
}

// Command wraps the argv in bwrap; folders that are not on the host are left out of the mounts.
func (b *Bubblewrap) Command(ctx context.Context, _ Instance, s Spec, c Command) (*exec.Cmd, error) {
	r, err := newRules(s)
	if err != nil {
		return nil, err
	}
	bin, err := exec.LookPath(b.opt.Binary)
	if err != nil {
		return nil, fmt.Errorf("%s is not installed: install the bubblewrap package", b.opt.Binary)
	}
	fi, lerr := os.Lstat("/var/run")
	varRunDir := lerr == nil && fi.IsDir()
	args := append(bwrapArgs(r.existing(), b.opt.Home, varRunDir), "--")
	cmd := confined(ctx, c.Timeout, bin, append(args, c.Argv...))
	cmd.Env = buildEnv(r, c, false)
	return cmd, nil
}

// Release has nothing to remove: the sandbox ends with each command.
func (b *Bubblewrap) Release(context.Context, Instance) error { return nil }

// Caveats names the limits Bubblewrap cannot enforce without delegated control groups.
func (b *Bubblewrap) Caveats(s Spec) []string { return osCaveats("Bubblewrap", s) }
