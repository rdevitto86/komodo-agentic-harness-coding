package cli

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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
	sc := bufio.NewScanner(f) // TODO - linter error "bufio.Scanner "sc" is used in Scan loop at line 129 without final check of sc.Err()"

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

// resolveRun validates the label of run and expands it to the group labels under docs/backlog/.
func resolveRun(c *Call) *Result {
	if c.Global.List || flagBool(c, "approve") || flagBool(c, "deny") {
		return nil
	}

	backlog := filepath.Join(c.Repo.Root, "docs", "backlog")
	if c.Global.All {
		c.Groups = openGroups(backlog)
		return nil
	}

	label := c.Args[0]
	groups, ok := groupsOf(backlog, label)

	if !ok {
		r := usageErr("no "+label+" under docs/backlog/", "check the label in docs/backlog/, or plan it with komodo plan")
		return &r
	}

	c.Groups = groups
	return nil
}

// groupsOf expands an epic label to its group labels, or confirms a group label exists.
func groupsOf(backlog, label string) ([]string, bool) {
	if epicLabel.MatchString(label) {
		dir := filepath.Join(backlog, "epic-"+strings.TrimPrefix(label, "ep-"))
		g := groupDirs(dir)
		if _, err := os.Stat(dir); err != nil {
			return nil, false
		}
		return g, true
	}

	epic, _, _ := strings.Cut(strings.TrimPrefix(label, "tg-"), ".")
	if _, err := os.Stat(filepath.Join(backlog, "epic-"+epic, label)); err != nil {
		return nil, false
	}
	return []string{label}, true
}

// groupDirs lists the tg-N.M folders of an epic folder in numeric order.
func groupDirs(epicDir string) []string {
	ents, _ := os.ReadDir(epicDir)
	var out []string

	for _, e := range ents {
		if e.IsDir() && groupLabel.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}

	sort.Slice(out, func(i, j int) bool { return groupKey(out[i]) < groupKey(out[j]) })
	return out
}

// groupKey turns tg-N.M into a number that sorts groups by epic, then by position.
func groupKey(label string) int {
	a, b, _ := strings.Cut(strings.TrimPrefix(label, "tg-"), ".")
	n, _ := strconv.Atoi(a)
	m, _ := strconv.Atoi(b)
	return n*100000 + m
}

// openGroups lists every group under the backlog whose TG.md does not say status: done.
func openGroups(backlog string) []string {
	epics, _ := filepath.Glob(filepath.Join(backlog, "epic-*"))
	var out []string

	for _, e := range epics {
		for _, g := range groupDirs(e) {
			if !groupDone(filepath.Join(e, g, "TG.md")) {
				out = append(out, g)
			}
		}
	}

	sort.Slice(out, func(i, j int) bool { return groupKey(out[i]) < groupKey(out[j]) })
	return out
}

// groupDone reports whether a TG.md holds the line status: done.
func groupDone(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}

	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)

	for sc.Scan() {
		if v, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "status:"); ok {
			return strings.TrimSpace(v) == "done"
		}
	}
	return false
}
