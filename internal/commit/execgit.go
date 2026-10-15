package commit

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

// ExecGit is the Git that runs the git program. Every command gets an explicit git folder found from a known
// clone, never from the pointer file inside a worktree, and a blank file-system monitor.
type ExecGit struct {
	Trace func(args []string) // called with every argument list before git runs; nil in production
	Bin   string              // the program to run; empty means git

	mu     sync.Mutex
	clones []cloneRef
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

// NewExecGit returns a Git that knows the given clones; more are learned from the clone arguments of later calls.
func NewExecGit(clones ...string) *ExecGit {
	g := &ExecGit{}
	for _, c := range clones {
		g.Adopt(c)
	}
	return g
}

// real resolves symbolic links so paths compare equal however they were spelled.
func real(p string) string {
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

// Adopt registers a clone, given as its working folder or its common git folder, so its worktrees can be found.
func (g *ExecGit) Adopt(clone string) {
	common, ok := commonOf(clone)
	if !ok {
		return
	}
	ref := cloneRef{common: real(common)}
	if filepath.Base(common) == ".git" && real(common) != real(clone) {
		ref.root = real(clone)
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

// target is where a command runs: the git folder and the working folder, empty for a bare repository.
type target struct{ gitDir, work string }

// locate finds the git folder of dir from the known clones: dir is a clone's working folder, or a worktree
// whose back-pointer file inside a clone's own git folder names dir.
func (g *ExecGit) locate(dir string) (target, error) {
	r := real(dir)
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
			if real(filepath.Dir(strings.TrimSpace(string(b)))) == r {
				return target{gitDir: filepath.Dir(p), work: dir}, nil
			}
		}
	}
	return target{}, fmt.Errorf("%s: %w", dir, ErrNoGitDir)
}

// ofClone resolves a clone argument, registering it, to a target without a working folder.
func (g *ExecGit) ofClone(clone string) (target, error) {
	g.Adopt(clone)
	common, ok := commonOf(clone)
	if !ok {
		return target{}, fmt.Errorf("%s: %w", clone, ErrNoGitDir)
	}
	return target{gitDir: common}, nil
}

// checkArgs refuses an argument list that would force, delete a remote reference or use a plus refspec.
func checkArgs(args []string) error {
	for _, a := range args {
		if a == "-f" || a == "-D" || a == "--delete" || strings.HasPrefix(a, "--force") || strings.HasPrefix(a, "+") || strings.HasPrefix(a, ":") {
			return fmt.Errorf("argument %q: %w", a, ErrForce)
		}
	}
	return nil
}

// cleanEnv drops the variables that would redirect git away from the explicit folder.
func cleanEnv(extra ...string) []string {
	var env []string
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		switch k {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_NAMESPACE":
			continue
		}
		env = append(env, e)
	}
	return append(env, append([]string{"GIT_TERMINAL_PROMPT=0", "LC_ALL=C"}, extra...)...)
}

// run executes git against the target with fixed settings and returns standard output.
func (g *ExecGit) run(ctx context.Context, t target, stdin string, env []string, args ...string) (string, error) {
	full := []string{"--git-dir=" + t.gitDir}
	if t.work != "" {
		full = append(full, "--work-tree="+t.work)
	}
	full = append(full, "-c", "core.fsmonitor=", "-c", "core.longpaths=true", "-c", "core.filemode=false", "-c", "core.autocrlf=false")
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
	cmd.Env = cleanEnv(env...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		ge := &gitError{cmd: strings.Join(args[:min(2, len(args))], " "), code: -1, stderr: strings.TrimSpace(errb.String())}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			ge.code = ee.ExitCode()
			return out.String(), ge
		}
		return "", fmt.Errorf("start git: %w", err)
	}
	return out.String(), nil
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

// validBranch reports whether name is a komodo/ branch with a safe spelling.
func validBranch(name string) bool {
	rest, ok := strings.CutPrefix(name, "komodo/")
	if !ok || rest == "" || strings.Contains(name, "..") || strings.Contains(name, "//") || strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".lock") {
		return false
	}
	return safeName(name)
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
func safeRev(s string) bool { return s != "" && !strings.HasPrefix(s, "-") && !strings.ContainsAny(s, "\x00\n") }

// currentBranch returns the branch name HEAD names in the target; a detached HEAD is ErrCriticalRef.
func (g *ExecGit) currentBranch(ctx context.Context, t target) (string, error) {
	out, err := g.run(ctx, t, "", nil, "symbolic-ref", "-q", "HEAD")
	if err != nil {
		if exitCode(err) == 1 {
			return "", fmt.Errorf("HEAD is detached: %w", ErrCriticalRef)
		}
		return "", err
	}
	name, ok := strings.CutPrefix(strings.TrimSpace(out), "refs/heads/")
	if !ok || !validBranch(name) {
		return "", fmt.Errorf("checked-out branch %q: %w", strings.TrimSpace(out), ErrCriticalRef)
	}
	return name, nil
}
