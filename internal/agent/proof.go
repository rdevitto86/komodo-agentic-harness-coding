package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// MaxProofBody is the most bytes a Reviewer-written proof test may hold.
const MaxProofBody = 64 << 10

// ErrProofRefused marks a proof Go will not run: a command off the allowed list, a bad argument or a bad test file.
var ErrProofRefused = errors.New("the proof is not allowed")

// Proof is the evidence a finding carries; exactly one of Cmd and Test is set.
type Proof struct {
	Cmd  string     `json:"cmd,omitempty"`  // an allowed command, run as written
	Test *ProofTest `json:"test,omitempty"` // a Reviewer-written test

	Expect string `json:"expect"` // a line the failing output must contain, such as the failing test's name
}

// ProofTest is a short test the Reviewer wrote to show the bug.
type ProofTest struct {
	File string `json:"file"` // a new test file, never an existing path
	Body string `json:"body"` // its whole text
	Run  string `json:"run"`  // the allowed command that runs it
}

// ProofResult is what Go saw when it ran a proof.
type ProofResult struct {
	Failed bool   // the command exited non-zero
	Output string // exact output, capped like a gate result
}

// ProofRunner copies the change into a throwaway folder, runs a proof there and discards the copy.
type ProofRunner func(ctx context.Context, p Proof) (ProofResult, error)

// RunProof reports failed_as_claimed, passed or none after one run of the proof. A proof with no command, no test or no
// expect line is none, because it cannot show the failure claimed.
func RunProof(ctx context.Context, run ProofRunner, p Proof) (string, error) {
	if (p.Cmd == "" && p.Test == nil) || strings.TrimSpace(p.Expect) == "" {
		return "none", nil
	}

	res, err := run(ctx, p)
	if err != nil {
		return "", err
	}
	if res.Failed && strings.Contains(res.Output, p.Expect) { // the failure is the one claimed
		return "failed_as_claimed", nil
	}

	return "passed", nil
}

// ProofCommands are the repository's own detected commands, as argument lists, that a proof may extend.
type ProofCommands struct {
	Build []string
	Test  []string
	Lint  []string
}

// goProofFlags lists, per allowed go subcommand, the flags a proof may pass and the pattern of each flag's value; a flag
// with no pattern takes no value.
var goProofFlags = map[string]map[string]*regexp.Regexp{
	"test": {
		"run":      regexp.MustCompile(`^[A-Za-z0-9_^$|().*/-]+$`),
		"race":     nil,
		"count":    regexp.MustCompile(`^[0-9]+$`),
		"v":        nil,
		"short":    nil,
		"failfast": nil,
		"timeout":  regexp.MustCompile(`^[0-9]+[smh]$`),
	},
	"vet":   {},
	"build": {},
}

var (
	// proofArgRe is the characters of one extra argument to a repository command; no quote, space, redirect or expansion.
	proofArgRe = regexp.MustCompile(`^[A-Za-z0-9_./=:,@^+-]+$`)
	// proofPkgRe is a relative package pattern or an import path.
	proofPkgRe = regexp.MustCompile(`^(\./)?([A-Za-z0-9_.-]+/)*[A-Za-z0-9_.-]*$`)
	// proofFileRe is the characters of a proof test's file path.
	proofFileRe = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)
	// proofDeniedFlags are flags that start another program or read or write outside the copy.
	proofDeniedFlags = []string{"exec", "toolexec", "overlay", "modfile", "c", "o", "config", "rootdir", "vettool"}
)

// ProofArgv checks a proof command against the allowed list and returns it as an argument list. The allowed forms are
// go test, go vet and go build with a short flag list, and the repository's own test, build or lint command with extra
// arguments. The command is never read by a shell: every token matches a fixed pattern.
func ProofArgv(cmd string, c ProofCommands) ([]string, error) {
	if strings.ContainsAny(cmd, "\r\n\t\x00") {
		return nil, fmt.Errorf("%w: the command holds a control character", ErrProofRefused)
	}

	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return nil, fmt.Errorf("%w: the command is empty", ErrProofRefused)
	}
	if fields[0] == "go" {
		return goProofArgv(fields)
	}

	for _, base := range [][]string{c.Test, c.Lint, c.Build} {
		if len(base) == 0 || len(fields) < len(base) || !slices.Equal(fields[:len(base)], base) {
			continue
		}
		for _, a := range fields[len(base):] {
			if err := checkProofArg(a); err != nil {
				return nil, err
			}
		}
		return fields, nil
	}

	return nil, fmt.Errorf("%w: %q is not a detected test, build or lint command", ErrProofRefused, fields[0])
}

// goProofArgv checks a go test, go vet or go build command.
func goProofArgv(fields []string) ([]string, error) {
	if len(fields) < 2 {
		return nil, fmt.Errorf("%w: go needs a subcommand", ErrProofRefused)
	}

	flags, ok := goProofFlags[fields[1]]
	if !ok {
		return nil, fmt.Errorf("%w: go %s is not allowed", ErrProofRefused, fields[1])
	}

	for i := 2; i < len(fields); i++ {
		a := fields[i]
		if !strings.HasPrefix(a, "-") {
			if !proofPkgRe.MatchString(a) || hasDotDot(a) {
				return nil, fmt.Errorf("%w: %q is not a package inside the repository", ErrProofRefused, a)
			}
			continue
		}

		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		re, known := flags[name]
		if !known {
			return nil, fmt.Errorf("%w: go %s flag %q is not allowed", ErrProofRefused, fields[1], a)
		}
		if re == nil {
			if hasVal {
				return nil, fmt.Errorf("%w: flag %q takes no value", ErrProofRefused, a)
			}
			continue
		}
		if !hasVal {
			if i++; i >= len(fields) {
				return nil, fmt.Errorf("%w: flag %q needs a value", ErrProofRefused, a)
			}
			val = fields[i]
		}
		if !re.MatchString(val) {
			return nil, fmt.Errorf("%w: %q is not a value of flag %q", ErrProofRefused, val, a)
		}
	}

	return fields, nil
}

// checkProofArg checks one extra argument to a repository command.
func checkProofArg(a string) error {
	if !proofArgRe.MatchString(a) {
		return fmt.Errorf("%w: argument %q holds a character that is not allowed", ErrProofRefused, a)
	}

	name, val, hasVal := strings.Cut(a, "=")
	if strings.HasPrefix(a, "-") {
		if slices.Contains(proofDeniedFlags, strings.ToLower(strings.TrimLeft(name, "-"))) {
			return fmt.Errorf("%w: flag %q is not allowed", ErrProofRefused, name)
		}
	} else {
		val, hasVal = a, true
	}
	if hasVal && (strings.HasPrefix(val, "/") || hasDotDot(val)) {
		return fmt.Errorf("%w: argument %q leaves the repository", ErrProofRefused, a)
	}

	return nil
}

// hasDotDot reports whether a slash path holds a .. segment.
func hasDotDot(p string) bool { return slices.Contains(strings.Split(p, "/"), "..") }

// ProofConfig is what the runner of a proof needs.
type ProofConfig struct {
	Run    CommandRunner // runs one shell line in the Reviewer's sandbox
	Source string        // the worktree that holds the work under test
	Root   string        // the folder that holds each throwaway copy; the sandbox must reach it
	Cmds   ProofCommands // the repository's detected commands
	// lists the absolute paths of the secret files the copy leaves out; nil copies every file
	Secrets func() ([]string, error)
}

// NewProofRunner returns the runner of a proof: it checks the proof, copies the worktree without .git into a new folder
// under Root, adds the proof's test file, runs the command there and removes the copy. The shell line holds only tokens
// ProofArgv accepted, each quoted.
func NewProofRunner(cfg ProofConfig) ProofRunner {
	return func(ctx context.Context, p Proof) (ProofResult, error) {
		argv, err := cfg.proofArgv(p)
		if err != nil {
			return ProofResult{}, err
		}

		dir, err := os.MkdirTemp(cfg.Root, "proof-")
		if err != nil {
			return ProofResult{}, fmt.Errorf("make the proof folder: %w", err)
		}
		defer func() { _ = os.RemoveAll(dir) }()

		var secrets []string
		if cfg.Secrets != nil {
			if secrets, err = cfg.Secrets(); err != nil {
				return ProofResult{}, fmt.Errorf("list the secret files: %w", err)
			}
		}
		if err := copyWorktree(cfg.Source, dir, secrets); err != nil {
			return ProofResult{}, err
		}
		if p.Test != nil {
			if err := writeProofTest(dir, *p.Test); err != nil {
				return ProofResult{}, err
			}
		}

		quoted := make([]string, len(argv))
		for i, a := range argv {
			quoted[i] = shQuote(a)
		}

		exit, out, err := cfg.Run(ctx, "cd "+shQuote(dir)+" && "+strings.Join(quoted, " "))
		if err != nil && !errors.Is(err, ErrOutputOverLimit) {
			return ProofResult{}, fmt.Errorf("run the proof: %w", err)
		}

		return ProofResult{Failed: exit != 0, Output: out}, nil
	}
}

// Check reports why Go would refuse a proof, or nil when it may run; the Reviewer sees the reason at submit.
func (cfg ProofConfig) Check(p Proof) error {
	_, err := cfg.proofArgv(p)
	return err
}

// proofArgv checks a whole proof: its expect line, its one command or test, and a test file that does not exist yet.
func (cfg ProofConfig) proofArgv(p Proof) ([]string, error) {
	switch {
	case strings.TrimSpace(p.Expect) == "":
		return nil, fmt.Errorf("%w: no expect line", ErrProofRefused)
	case p.Cmd != "" && p.Test != nil:
		return nil, fmt.Errorf("%w: both a command and a test", ErrProofRefused)
	case p.Test == nil:
		return ProofArgv(p.Cmd, cfg.Cmds)
	}

	f := p.Test.File
	if f == "" || len(f) > 200 || !proofFileRe.MatchString(f) || path.Clean(f) != f || path.IsAbs(f) || hasDotDot(f) ||
		slices.ContainsFunc(strings.Split(f, "/"), func(s string) bool { return strings.HasPrefix(s, ".") }) {
		return nil, fmt.Errorf("%w: %q is not a new file path inside the repository", ErrProofRefused, f)
	}
	if len(p.Test.Body) > MaxProofBody {
		return nil, fmt.Errorf("%w: the test file is over %d bytes", ErrProofRefused, MaxProofBody)
	}
	if _, err := os.Lstat(filepath.Join(cfg.Source, filepath.FromSlash(f))); err == nil {
		return nil, fmt.Errorf("%w: %s exists", ErrProofRefused, f)
	}

	return ProofArgv(p.Test.Run, cfg.Cmds)
}

// writeProofTest writes a proof's test file into the copy; it never replaces a file.
func writeProofTest(dir string, t ProofTest) error {
	full := filepath.Join(dir, filepath.FromSlash(t.File))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("make the proof test folder: %w", err)
	}

	f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create the proof test file: %w", err)
	}
	if _, err := io.WriteString(f, t.Body); err != nil {
		_ = f.Close()
		return fmt.Errorf("write the proof test file: %w", err)
	}

	return f.Close()
}

// copyWorktree copies the regular files and folders of a worktree into an existing folder, but not .git and not the files
// in skip (absolute paths).
func copyWorktree(src, dst string, skip []string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if slices.Contains(skip, p) {
			return nil
		}

		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}

		switch {
		case rel == ".":
			return nil
		case d.IsDir() && d.Name() == ".git":
			return filepath.SkipDir
		case d.IsDir():
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		case !d.Type().IsRegular():
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		return copyFile(p, filepath.Join(dst, rel), info.Mode().Perm())
	})
}

// copyFile copies one file's bytes with the given permissions.
func copyFile(from, to string, perm fs.FileMode) error {
	in, err := os.Open(from)
	if err != nil {
		return fmt.Errorf("copy %s: %w", from, err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return fmt.Errorf("copy %s: %w", from, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy %s: %w", from, err)
	}

	return out.Close()
}
