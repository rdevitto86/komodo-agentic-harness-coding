package task

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Pointer is the exact text of a worktree's .git file, recorded at the start of a group.
type Pointer struct {
	file string
	text []byte
}

// HostGit runs git on the host for one worktree under the host git rules: pointer check, explicit
// folders, blank hooks and monitors.
type HostGit struct {
	lease LeaseRef
	ptr   *Pointer
	base  string
	empty string // an empty folder: hooks path and home
	bin   string
}

// ErrPointerChanged is returned when the worktree's .git pointer file no longer holds the text
// recorded at the start.
var ErrPointerChanged = errors.New("the .git pointer of the worktree changed")

// absPath finds a host path inside a message.
var absPath = regexp.MustCompile(`(^|[\s'"(=])/[^\s'")]+`)

// gitReadFlags are the flags that keep each read-only command from running a converter.
var gitReadFlags = map[string][]string{
	"log":   {"--no-textconv", "--no-ext-diff"},
	"show":  {"--no-textconv", "--no-ext-diff"},
	"blame": {"--no-textconv"},
}

// NewHostGit builds the runner for a lease. base is the commit the group's diff is taken against;
// empty means HEAD.
func NewHostGit(l LeaseRef, base string, ptr *Pointer) (*HostGit, error) {
	if l.Worktree == "" || l.GitDir == "" || ptr == nil {
		return nil, errors.New(
			"host git needs the lease's worktree, its git folder and the recorded pointer",
		)
	}

	bin, err := lookGit()
	if err != nil {
		return nil, err
	}

	empty, err := os.MkdirTemp("", "komodo-git-empty-")
	if err != nil {
		return nil, fmt.Errorf("make the empty hooks folder: %w", err)
	}

	return &HostGit{lease: l, ptr: ptr, base: cmpOr(base, "HEAD"), empty: empty, bin: bin}, nil
}

// Check reads the pointer file again and fails with ErrPointerChanged when it differs or cannot be
// read.
func (p *Pointer) Check() error {
	now, err := os.ReadFile(p.file)
	if err != nil || !bytes.Equal(now, p.text) {
		return ErrPointerChanged
	}

	return nil
}

// Close removes the runner's temporary folder.
func (h *HostGit) Close() error { return os.RemoveAll(h.empty) }

// Run checks the pointer, then runs one git command and returns its output.
func (h *HostGit) Run(ctx context.Context, rest ...string) (string, error) {
	return h.run(ctx, nil, rest...)
}

// ReadOnly runs log, show, blame or status; the guard has already checked the arguments.
func (h *HostGit) ReadOnly(ctx context.Context, command string, args []string) (string, error) {
	switch command {
	case "log", "show", "blame", "status":
	default:
		return "", fmt.Errorf("git %s is not a read-only command the harness runs", command)
	}

	flags := gitReadFlags[command]

	return h.Run(ctx, append(append([]string{command}, flags...), args...)...)
}

// Diff returns the unified diff of the worktree, new files included, against the base commit.
func (h *HostGit) Diff(ctx context.Context, paths []string) (string, error) {
	var out string

	err := h.snapshot(ctx, func(env []string) error {
		args := []string{"diff", "--cached", "--no-ext-diff", "--no-textconv", h.base}
		if len(paths) > 0 {
			args = append(append(args, "--"), paths...)
		}

		var err error

		out, err = h.run(ctx, env, args...)

		return err
	})

	return out, err
}

// TreeHash returns the hash of the tree the worktree holds now, untracked files included and
// ignored ones left out.
func (h *HostGit) TreeHash(ctx context.Context) (string, error) {
	var out string

	err := h.snapshot(ctx, func(env []string) error {
		var err error

		out, err = h.run(ctx, env, "write-tree")

		return err
	})

	return strings.TrimSpace(out), err
}

// args puts the explicit folders and the blank settings in front of a git command.
func (h *HostGit) args(rest ...string) []string {
	return append([]string{
		h.bin,
		"--git-dir=" + h.lease.GitDir,
		"--work-tree=" + h.lease.Worktree,
		"-c", "core.fsmonitor=",
		"-c", "core.hooksPath=" + h.empty,
		"-c", "core.pager=cat",
		"-c", "diff.external=",
	}, rest...)
}

// env is the only environment a git call gets.
func (h *HostGit) env(extra ...string) []string {
	return append(
		[]string{"PATH=" + os.Getenv("PATH"), "HOME=" + h.empty, "LANG=C", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0"},
		extra...)
}

// run is Run with extra environment variables.
func (h *HostGit) run(ctx context.Context, extraEnv []string, rest ...string) (string, error) {
	if err := h.ptr.Check(); err != nil {
		return "", err
	}

	out, err := hostOutput(
		ctx,
		h.lease.Worktree,
		h.env(extraEnv...),
		"git "+rest[0],
		h.args(rest...)...)
	if err != nil {
		return string(out), errors.New(h.scrub(err.Error(), extraEnv))
	}

	return string(out), nil
}

// scrub removes the host's folders from a message that may reach the model: the git folder, the
// worktree, the temporary folders and any other absolute path.
func (h *HostGit) scrub(msg string, extraEnv []string) string {
	pairs := []string{
		h.lease.GitDir,
		"<git dir>",
		h.commonDir(),
		"<git dir>",
		h.lease.Worktree,
		"<worktree>",
		h.empty,
		"<tmp>",
	}

	for _, kv := range extraEnv {
		if _, v, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(v, "/") {
			pairs = append(pairs, v, "<tmp>")
		}
	}

	msg = strings.NewReplacer(pairs...).Replace(msg)

	return absPath.ReplaceAllString(msg, "${1}<path>")
}

// snapshot builds a throw-away index and object folder holding the worktree's current files, so the
// real index and objects stay as they are.
func (h *HostGit) snapshot(ctx context.Context, use func(env []string) error) error {
	dir, err := os.MkdirTemp("", "komodo-git-snap-")
	if err != nil {
		return fmt.Errorf("make the snapshot folder: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	env := []string{"GIT_INDEX_FILE=" + filepath.Join(dir, "index"), "GIT_OBJECT_DIRECTORY=" + dir,
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + filepath.Join(h.commonDir(), "objects")}
	if _, err := h.run(ctx, env, "read-tree", h.base); err != nil {
		return err
	}

	if _, err := h.run(ctx, env, "add", "-A"); err != nil {
		return err
	}

	return use(env)
}

// commonDir returns the shared git folder a worktree's own git folder points to.
func (h *HostGit) commonDir() string {
	b, err := os.ReadFile(filepath.Join(h.lease.GitDir, "commondir"))
	if err != nil {
		return h.lease.GitDir
	}

	rel := strings.TrimSpace(string(b))
	if filepath.IsAbs(rel) {
		return rel
	}

	return filepath.Join(h.lease.GitDir, rel)
}

// RecordPointer reads and keeps the exact text of <worktree>/.git.
func RecordPointer(worktree string) (*Pointer, error) {
	file := filepath.Join(worktree, ".git")

	text, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("record the .git pointer: %w", err)
	}

	return &Pointer{file: file, text: text}, nil
}

// lookGit finds the git program on the host's search path.
func lookGit() (string, error) {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		p := filepath.Join(dir, "git")
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}

	return "", errors.New("git was not found on the search path")
}
