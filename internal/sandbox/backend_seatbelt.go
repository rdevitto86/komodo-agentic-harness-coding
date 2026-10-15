package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"time"
)

// SeatbeltOptions override where Seatbelt finds its program, the person's home folder and the operating system; zero values use the host's.
type SeatbeltOptions struct {
	Binary string // sandbox-exec
	Home   string // closed to the sandbox except for the listed folders
	GOOS   string
}

// Seatbelt confines a command with a macOS sandbox profile.
type Seatbelt struct {
	opt      SeatbeltOptions
	mu       sync.Mutex
	profiles map[string]renderedProfile
}

// renderedProfile is the profile of one instance and the Spec it was rendered from.
type renderedProfile struct {
	spec Spec
	text string
}

// NewSeatbelt returns the Seatbelt backend.
func NewSeatbelt(o SeatbeltOptions) *Seatbelt {
	if o.Binary == "" {
		o.Binary = "sandbox-exec"
	}
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.Home == "" {
		o.Home, _ = os.UserHomeDir()
	}
	return &Seatbelt{opt: o, profiles: map[string]renderedProfile{}}
}

// Name returns the mechanism name.
func (s *Seatbelt) Name() string { return "seatbelt" }

// Check reports whether sandbox-exec can run here.
func (s *Seatbelt) Check() error {
	if s.opt.GOOS != "darwin" {
		return errors.New("Seatbelt runs on macOS only: use Bubblewrap on Linux or install Docker")
	}
	if _, err := exec.LookPath(s.opt.Binary); err != nil {
		return fmt.Errorf("%s is missing: restore /usr/bin/sandbox-exec or install Docker", s.opt.Binary)
	}
	return nil
}

// Prepare renders the profile once and has sandbox-exec accept it before any command depends on it.
func (s *Seatbelt) Prepare(ctx context.Context, sp Spec) (Instance, error) {
	r, err := newRules(sp)
	if err != nil {
		return Instance{}, err
	}
	text := seatbeltProfile(r, s.opt.Home)
	bin, err := exec.LookPath(s.opt.Binary)
	if err != nil {
		return Instance{}, fmt.Errorf("%s is missing: restore /usr/bin/sandbox-exec or install Docker", s.opt.Binary)
	}
	vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(vctx, bin, "-p", text, "/usr/bin/true")
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return Instance{}, fmt.Errorf("seatbelt rejected the profile: %s", firstLine(stderr.String(), err))
	}
	inst, err := newInstance(sp.Group, s.Name())
	if err != nil {
		return Instance{}, err
	}
	s.mu.Lock()
	s.profiles[inst.ID] = renderedProfile{spec: cloneSpec(sp), text: text}
	s.mu.Unlock()
	return inst, nil
}

// Command wraps the argv in sandbox-exec with the instance's profile, which is rendered again only when the Spec changed.
func (s *Seatbelt) Command(ctx context.Context, i Instance, sp Spec, c Command) (*exec.Cmd, error) {
	r, err := newRules(sp)
	if err != nil {
		return nil, err
	}
	bin, err := exec.LookPath(s.opt.Binary)
	if err != nil {
		return nil, fmt.Errorf("%s is missing: restore /usr/bin/sandbox-exec or install Docker", s.opt.Binary)
	}
	s.mu.Lock()
	cur, ok := s.profiles[i.ID]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("instance %s was not prepared", i.ID)
	}
	if !reflect.DeepEqual(cur.spec, sp) {
		cur = renderedProfile{spec: cloneSpec(sp), text: seatbeltProfile(r, s.opt.Home)}
		s.profiles[i.ID] = cur
	}
	s.mu.Unlock()
	cmd := confined(ctx, c.Timeout, bin, append([]string{"-p", cur.text}, c.Argv...))
	cmd.Dir = r.Worktree
	cmd.Env = buildEnv(r, c, false)
	return cmd, nil
}

// Release forgets the instance's profile.
func (s *Seatbelt) Release(_ context.Context, i Instance) error {
	s.mu.Lock()
	delete(s.profiles, i.ID)
	s.mu.Unlock()
	return nil
}

// Caveats names the limits Seatbelt cannot enforce: it has no process or memory cap.
func (s *Seatbelt) Caveats(sp Spec) []string { return osCaveats("Seatbelt", sp) }

// osCaveats names the process and memory caps an operating-system sandbox without control groups cannot apply.
func osCaveats(name string, sp Spec) []string {
	var out []string
	if sp.MaxProcs > 0 {
		out = append(out, fmt.Sprintf("%s cannot cap processes: sandbox.max_procs=%d is not enforced", name, sp.MaxProcs))
	}
	if sp.MaxMemGB > 0 {
		out = append(out, fmt.Sprintf("%s cannot cap memory: sandbox.max_mem_gb=%d is not enforced", name, sp.MaxMemGB))
	}
	return out
}

// firstLine returns the first non-empty line of a program's error output, or the Go error when it printed none.
func firstLine(out string, err error) string {
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return err.Error()
}
