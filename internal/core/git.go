package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// Refusal is a git operation the wrapper declined on purpose; its Refused method answers true so callers can tell it from a
// failure.
type Refusal struct{ msg string }

// Error returns the reason for the refusal.
func (r *Refusal) Error() string { return r.msg }

// Refused marks the error as a refusal on purpose.
func (r *Refusal) Refused() bool { return true }

// CommitInfo describes one commit.
type CommitInfo struct {
	ID      string `json:"id"`      // commit id
	Tree    string `json:"tree"`    // tree hash of the commit
	Parent  string `json:"parent"`  // first parent's id; empty for a root commit
	Second  string `json:"second"`  // second parent's id; empty unless the commit is a merge
	Subject string `json:"subject"` // first line of the message
}

// Landing is one group landing on an epic branch, read back from its merge commit.
type Landing struct {
	Label   string `json:"label"`   // group label, from the subject
	Goal    string `json:"goal"`    // group goal, from the subject
	Section string `json:"section"` // the group's section of the pull request body, from the message body
}

// SectionMarker is the first line of a group's section; only a merge commit whose body starts with it is a landing.
const SectionMarker = "## Summary"

// Git runs every git command of the harness. It has no force option and no refspec argument: each command gets an explicit
// git folder found from a known clone, never from the pointer file inside
// a worktree, no repository hooks, a blank file-system monitor and a scrubbed environment.
type Git struct {
	Trace func(args []string) // called with every argument list before git runs; nil in production
	Bin   string              // the program to run; empty means git

	mu     sync.Mutex
	clones []cloneRef

	hooksOnce sync.Once
	hooksDir  string
}

// NewGit returns a Git that knows the given clones; more are learned from the clone arguments of later calls.
func NewGit(clones ...string) *Git {
	g := &Git{}
	for _, c := range clones {
		g.Adopt(c)
	}
	return g
}

// Adopt registers a clone, given as its working folder or its common git folder, so its worktrees can be found.
func (g *Git) Adopt(clone string) {
	common, ok := commonOf(clone)
	if !ok {
		return
	}

	ref := cloneRef{common: realPath(common)}
	if filepath.Base(common) == ".git" && realPath(common) != realPath(clone) {
		ref.root = realPath(clone)
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	for i, c := range g.clones {
		if c.common == ref.common {
			if c.root == "" {
				g.clones[i].root = ref.root
			}
			return
		}
	}

	g.clones = append(g.clones, ref)
}

// CurrentBranch returns the short name of the branch HEAD names in the worktree; a detached HEAD or a branch outside komodo/
// is ErrCriticalRef.
func (g *Git) CurrentBranch(ctx context.Context, worktree string) (string, error) {
	t, err := g.locate(worktree)
	if err != nil {
		return "", err
	}
	return g.currentBranch(ctx, t)
}

// locate finds the git folder of dir from the known clones: dir is a clone's working folder, or a worktree whose
// back-pointer file inside a clone's own git folder names dir.
func (g *Git) locate(dir string) (target, error) {
	r := realPath(dir)

	g.mu.Lock()
	clones := append([]cloneRef(nil), g.clones...)
	g.mu.Unlock()

	for _, c := range clones {
		if c.root != "" && c.root == r {
			return target{gitDir: c.common, work: dir}, nil
		}
	}

	for _, c := range clones {
		pointers, _ := filepath.Glob(filepath.Join(c.common, "worktrees", "*", "gitdir"))
		for _, p := range pointers {
			b, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			if realPath(filepath.Dir(strings.TrimSpace(string(b)))) == r {
				return target{gitDir: filepath.Dir(p), work: dir}, nil
			}
		}
	}

	return target{}, fmt.Errorf("%s: %w", dir, ErrNoGitDir)
}

// ofClone resolves a clone argument, registering it, to a target without a working folder.
func (g *Git) ofClone(clone string) (target, error) {
	g.Adopt(clone)
	common, ok := commonOf(clone)
	if !ok {
		return target{}, fmt.Errorf("%s: %w", clone, ErrNoGitDir)
	}
	return target{gitDir: common}, nil
}

// runNet executes a git command that talks to the remote, with the person's login variables.
func (g *Git) runNet(ctx context.Context, t target, args ...string) (string, error) {
	return g.exec(ctx, t, true, "", nil, args...)
}

// exec runs git against the target with fixed settings, no hooks and a scrubbed environment.
func (g *Git) exec(ctx context.Context, t target, net bool, stdin string, env []string, args ...string) (string, error) {
	full := []string{"--git-dir=" + t.gitDir}
	if t.work != "" {
		full = append(full, "--work-tree="+t.work)
	}

	full = append(
		full,
		"-c", "core.hooksPath="+g.emptyHooks(),
		"-c", "core.fsmonitor=",
		"-c", "core.longpaths=true",
		"-c", "core.filemode=false",
		"-c", "core.autocrlf=false",
		"-c", "protocol.ext.allow=never",
	)

	full = append(full, args...)
	if err := checkArgs(full); err != nil {
		return "", err
	}
	if g.Trace != nil {
		g.Trace(append([]string(nil), full...))
	}

	bin := g.Bin
	if bin == "" {
		bin = "git"
	}

	cmd := exec.CommandContext(ctx, bin, full...)
	cmd.Dir = t.work
	if cmd.Dir == "" {
		cmd.Dir = t.gitDir
	}

	cmd.Env = scrubbedEnv(net, env...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}

	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		ge := &gitError{
			cmd:    strings.Join(args[:min(2, len(args))], " "),
			code:   -1,
			stderr: strings.TrimSpace(errb.String()),
		}

		var ee *exec.ExitError
		if errors.As(err, &ee) {
			ge.code = ee.ExitCode()
			return out.String(), ge
		}

		return "", fmt.Errorf("start git: %w", err)
	}

	return out.String(), nil
}

// emptyHooks returns a folder with no hooks in it, created once.
func (g *Git) emptyHooks() string {
	g.hooksOnce.Do(func() {
		d, err := os.MkdirTemp("", "komodo-nohooks-")
		if err != nil {
			d = filepath.Join(os.TempDir(), "komodo-nohooks-missing")
		}
		g.hooksDir = d
	})
	return g.hooksDir
}

// currentBranch returns the komodo/ branch HEAD names in the target.
func (g *Git) currentBranch(ctx context.Context, t target) (string, error) {
	out, err := g.run(ctx, t, "", nil, "symbolic-ref", "-q", "HEAD")
	if err != nil {
		if exitCode(err) == 1 {
			return "", fmt.Errorf("HEAD is detached: %w", ErrCriticalRef)
		}
		return "", err
	}

	name, ok := strings.CutPrefix(strings.TrimSpace(out), "refs/heads/")
	if !ok || !ValidBranch(name) {
		return "", fmt.Errorf("checked-out branch %q: %w", strings.TrimSpace(out), ErrCriticalRef)
	}

	return name, nil
}

// run executes a git command that stays on this machine and returns standard output.
func (g *Git) run(ctx context.Context, t target, stdin string, env []string, args ...string) (string, error) {
	return g.exec(ctx, t, false, stdin, env, args...)
}

// cloneRef is a known clone: its working folder (empty when unknown) and its common git folder.
type cloneRef struct{ root, common string }

// gitError is a failed git command with its standard error text.
type gitError struct {
	cmd    string
	code   int
	stderr string
}

// Error names the command, its exit code and what git printed.
func (e *gitError) Error() string {
	return fmt.Sprintf("git %s: exit %d: %s", e.cmd, e.code, e.stderr)
}

// target is where a command runs: the git folder and the working folder, empty for a bare repository.
type target struct{ gitDir, work string }

var (
	ErrCriticalRef   = &Refusal{"reference is outside refs/heads/komodo/ and refs/komodo/"}
	ErrDirty         = &Refusal{"worktree holds files the tip lacks"}
	ErrTipMoved      = &Refusal{"branch no longer points at the expected commit"}
	ErrNotContained  = &Refusal{"commit is not in the history of the other reference"}
	ErrForce         = &Refusal{"git arguments would force or delete a remote reference"}
	ErrConflict      = errors.New("merge conflict")
	ErrPushRejected  = errors.New("remote rejected the push")
	ErrTrailer       = errors.New("commit message ends in trailer lines")
	ErrBranchMissing = errors.New("remote has no such branch")
	ErrNoGitDir      = errors.New("worktree is not registered with a known clone")
	ErrEmptyMessage  = errors.New("commit message is empty")
)

var (
	// baseEnv names the variables every git command receives.
	baseEnv = map[string]bool{
		"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "TMPDIR": true, "TMP": true,
		"TEMP": true, "LANG": true, "SYSTEMROOT": true, "USERPROFILE": true, "APPDATA": true,
		"LOCALAPPDATA": true, "XDG_CONFIG_HOME": true, "GIT_CONFIG_GLOBAL": true,
		"GIT_EXEC_PATH": true,
	}

	// netEnv names the extra variables a command that talks to the remote receives: the person's own login.
	netEnv = map[string]bool{
		"SSH_AUTH_SOCK": true, "GH_TOKEN": true, "GITHUB_TOKEN": true, "GH_ENTERPRISE_TOKEN": true,
		"GH_HOST": true, "GIT_ASKPASS": true, "SSH_ASKPASS": true, "GIT_SSH": true,
		"GIT_SSH_COMMAND": true, "HTTPS_PROXY": true, "HTTP_PROXY": true, "NO_PROXY": true,
		"ALL_PROXY": true, "https_proxy": true, "http_proxy": true, "no_proxy": true,
		"all_proxy": true,
	}
)

// ValidBranch reports whether name is a komodo/ branch with a safe spelling.
func ValidBranch(name string) bool {
	rest, ok := strings.CutPrefix(name, "komodo/")
	if !ok || rest == "" || strings.Contains(name, "..") || strings.Contains(name, "//") ||
		strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".lock") {
		return false
	}
	return safeName(name)
}

// realPath resolves symbolic links so paths compare equal however they were spelled.
func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// commonOf returns the common git folder of a clone root or of a git folder itself.
func commonOf(p string) (string, bool) {
	if fi, err := os.Stat(filepath.Join(p, ".git")); err == nil && fi.IsDir() {
		return filepath.Join(p, ".git"), true
	}
	if _, err := os.Stat(filepath.Join(p, "HEAD")); err == nil {
		if _, err := os.Stat(filepath.Join(p, "objects")); err == nil {
			return p, true
		}
	}
	return "", false
}

// checkArgs refuses an argument list that would force, delete a remote reference or use a plus refspec. A fetch may force
// its remote-tracking references, which only the harness reads.
func checkArgs(args []string) error {
	sub := subcommand(args)
	for _, a := range args {
		switch {
		case a == "-f" || a == "-D" || a == "--delete" ||
			strings.HasPrefix(a, "--force") || strings.HasPrefix(a, ":"):
		case strings.HasPrefix(a, "+"):
			if sub == "fetch" && strings.HasPrefix(a, "+refs/heads/") && strings.Contains(a, ":refs/remotes/") {
				continue
			}
		default:
			continue
		}

		return fmt.Errorf("argument %q: %w", a, ErrForce)
	}

	return nil
}

// subcommand returns the git subcommand of an argument list, skipping the leading -c pairs and --options.
func subcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-c":
			i++
		case strings.HasPrefix(args[i], "-"):
		default:
			return args[i]
		}
	}

	return ""
}

// scrubbedEnv builds the environment of one git command: the base variables, the login variables when net is true, no system
// configuration and no prompt.
func scrubbedEnv(net bool, extra ...string) []string {
	var env []string
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		if baseEnv[k] || (net && netEnv[k]) {
			env = append(env, e)
		}
	}
	env = append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	return append(env, extra...)
}

// exitCode returns the exit code of a failed git command, or -2 when err is not one.
func exitCode(err error) int {
	var ge *gitError
	if errors.As(err, &ge) {
		return ge.code
	}
	return -2
}

// stderrOf returns what a failed git command printed.
func stderrOf(err error) string {
	var ge *gitError
	if errors.As(err, &ge) {
		return ge.stderr
	}
	return ""
}

// safeName reports whether s has no character git or the shell treats specially in a reference name.
func safeName(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") || strings.Contains(s, "@{") {
		return false
	}

	for _, r := range s {
		if r <= ' ' || r == 0x7f || strings.ContainsRune("~^:?*[\\", r) {
			return false
		}
	}

	return true
}

// safeRev reports whether s may be passed to git as a revision.
func safeRev(s string) bool {
	return s != "" && !strings.HasPrefix(s, "-") && !strings.ContainsAny(s, "\x00\n")
}
