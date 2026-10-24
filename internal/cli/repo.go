package cli

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/format"
)

// pinPattern finds the repository pin in the Komodo block's begin marker.
var pinPattern = regexp.MustCompile(`komodo:begin min=([^\s>]+)`)

// findRepoRoot walks up from start to the first folder holding .git.
func findRepoRoot(start string) (string, bool) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", false
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// readPin returns the pin in the repository's AGENTS.md, read with no git process.
func readPin(root string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		return "", false
	}
	m := pinPattern.FindSubmatch(b)
	if m == nil {
		return "", false
	}
	return string(m[1]), true
}

// checkPin refuses when the repository pin is above the running version.
func (c *CLI) checkPin(call *Call) *Result {
	root, ok := findRepoRoot(call.dir())
	if !ok {
		return nil
	}

	pin, ok := readPin(root)
	if !ok {
		return nil
	}

	meets, err := core.BinaryMeetsPin(pin)
	if err != nil || meets {
		return nil
	}

	r := refuse(format.Pinned, "repository pinned to komodo "+pin, "run komodo update")
	return &r
}

// gitDir returns the git directory of a repository root; a worktree's .git file names it.
func gitDir(root string) string {
	p := filepath.Join(root, ".git")
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() {
		return p
	}

	b, err := os.ReadFile(p)
	if err != nil {
		return p
	}

	d := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "gitdir:"))
	if !filepath.IsAbs(d) {
		d = filepath.Join(root, d)
	}

	return d
}

// commonDir returns the git common directory, shared by every worktree of the repository.
func commonDir(root string) string {
	d := gitDir(root)
	b, err := os.ReadFile(filepath.Join(d, "commondir"))
	if err != nil {
		return d
	}

	c := strings.TrimSpace(string(b))
	if !filepath.IsAbs(c) {
		c = filepath.Join(d, c)
	}

	return filepath.Clean(c)
}

// repoID returns the normalized origin address, or the git common directory path with no remote.
func repoID(root string) string {
	common := commonDir(root)
	if url := originURL(filepath.Join(common, "config")); url != "" {
		return normalizeRemote(url)
	}
	return common
}

// RepoID returns the repository id of the repository at root, as the commands name it in a run-start.
func RepoID(root string) string { return repoID(root) }

// originURL reads the url of [remote "origin"] from a git config file.
func originURL(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}

	defer func() { _ = f.Close() }()
	in := false
	sc := bufio.NewScanner(f)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			in = line == `[remote "origin"]`
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok && in && strings.TrimSpace(k) == "url" {
			return strings.TrimSpace(v)
		}
	}
	if sc.Err() != nil {
		return ""
	}
	return ""
}

// normalizeRemote turns a remote address into host/owner/name with no scheme, user or .git suffix.
func normalizeRemote(url string) string {
	if i := strings.Index(url, "://"); i >= 0 {
		url = url[i+3:]
	} else if i := strings.Index(url, ":"); i >= 0 && !strings.Contains(url[:i], "/") {
		url = url[:i] + "/" + url[i+1:]
	}
	if i := strings.Index(url, "@"); i >= 0 && i < strings.Index(url+"/", "/") {
		url = url[i+1:]
	}
	return strings.TrimSuffix(strings.TrimSuffix(url, "/"), ".git")
}
