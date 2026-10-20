package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// rules are the mounts, masks and limits every mechanism applies, taken from one Spec.
type rules struct {
	Worktree   string
	Temp       string   // the instance's temporary folder: HOME and TMPDIR
	Writable   []string // the temporary folder and the private build caches
	ReadOnly   []string // paths inside the worktree that stay read-only, always including .git
	Readable   []string // the git folder, the module caches and the toolchains
	Toolchains []string // the toolchain subset of Readable
	GitDir     string
	Modules    string
	Masked     []string // files that read as empty or not at all
	MaxProcs   int
	MaxMemGB   int
	Network    bool
	Env        []string // variables beyond the allowed list; set only while Network is true
}

// runFolders are the host service-socket folders a shared-filesystem mechanism hides.
var runFolders = []string{"/private/var/run", "/var/run", "/run"}

// passEnv are the variables a command may bring; HOME, TMPDIR and PATH are set by the sandbox.
var passEnv = map[string]bool{
	"LANG": true, "RUSTUP_HOME": true, "CARGO_HOME": true, "CARGO_TARGET_DIR": true,
	"GOCACHE": true, "GOMODCACHE": true, "GOPATH": true, "XDG_CACHE_HOME": true,
	"npm_config_cache": true, "NPM_CONFIG_CACHE": true, "YARN_CACHE_FOLDER": true,
	"PNPM_STORE_DIR": true, "UV_CACHE_DIR": true, "PIP_CACHE_DIR": true,
}

// machServices are the system services a build or a test may look up; every other lookup is refused.
var machServices = []string{
	"com.apple.system.logger", "com.apple.logd", "com.apple.diagnosticd",
	"com.apple.system.notification_center", "com.apple.cfprefsd.daemon", "com.apple.cfprefsd.agent",
	"com.apple.system.opendirectoryd.libinfo", "com.apple.system.opendirectoryd.membership",
	"com.apple.system.DirectoryService.libinfo_v1",
	"com.apple.system.DirectoryService.membership_v1",
	"com.apple.bsd.dirhelper", "com.apple.FSEvents", "com.apple.SecurityServer", "com.apple.trustd",
	"com.apple.trustd.agent", "com.apple.coreservices.launchservicesd", "com.apple.lsd.mapdb",
}

// newRules validates a Spec and turns it into rules; it refuses anything a mechanism could not mount safely.
func newRules(s Spec) (rules, error) {
	if s.Group == "" {
		return rules{}, errors.New("the sandbox spec has no group")
	}
	if s.Role != RoleBuilder && s.Role != RoleTester {
		return rules{}, fmt.Errorf("unknown sandbox role %q", s.Role)
	}
	if s.MaxProcs < 0 || s.MaxMemGB < 0 {
		return rules{}, errors.New("sandbox limits cannot be negative")
	}

	var (
		r   rules
		err error
	)
	if r.Worktree, err = cleanPath("worktree", s.Worktree); err != nil {
		return rules{}, err
	}
	if len(s.Writable) == 0 {
		return rules{}, errors.New("the sandbox spec has no temporary folder: the first Writable entry is it")
	}
	if r.Writable, err = cleanAll("writable folder", s.Writable); err != nil {
		return rules{}, err
	}

	r.Temp = r.Writable[0]

	ro, err := cleanAll("read-only path", s.ReadOnly)
	if err != nil {
		return rules{}, err
	}

	r.ReadOnly = dedupe(append(ro, filepath.Join(r.Worktree, ".git")))
	if s.GitDir != "" {
		if r.GitDir, err = cleanPath("git folder", s.GitDir); err != nil {
			return rules{}, err
		}
		r.Readable = append(r.Readable, r.GitDir)
	}
	if s.Modules != "" {
		if r.Modules, err = cleanPath("module cache", s.Modules); err != nil {
			return rules{}, err
		}
		r.Readable = append(r.Readable, r.Modules)
	}
	if r.Toolchains, err = cleanAll("toolchain folder", s.Toolchains); err != nil {
		return rules{}, err
	}

	r.Readable = append(r.Readable, r.Toolchains...)
	if s.Network {
		r.Env = slices.Clone(s.SetupEnv)
	}

	r.Readable = dedupe(r.Readable)
	if r.Masked, err = cleanAll("masked file", s.Masked); err != nil {
		return rules{}, err
	}

	r.MaxProcs, r.MaxMemGB, r.Network = s.MaxProcs, s.MaxMemGB, s.Network
	return r, nil
}

// existing returns the rules without the read-only paths, readable folders and masks that are not on disk.
func (r rules) existing() rules {
	r.ReadOnly = onDisk(r.ReadOnly)
	r.Readable = onDisk(r.Readable)
	r.Toolchains = onDisk(r.Toolchains)
	r.Masked = onDisk(r.Masked)
	return r
}

// cleanAll cleans a list of paths and drops repeats.
func cleanAll(what string, ps []string) ([]string, error) {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		c, err := cleanPath(what, p)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return dedupe(out), nil
}

// cleanPath returns the cleaned form of an absolute path without characters that break a mount or a profile.
func cleanPath(what, p string) (string, error) {
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%s %q is not an absolute path", what, p)
	}
	if strings.ContainsAny(p, ":\n\r\x00") {
		return "", fmt.Errorf("%s %q holds a colon, a line break or a null byte", what, p)
	}
	return filepath.Clean(p), nil
}

// dedupe drops repeated entries and keeps the first of each, in order.
func dedupe(ps []string) []string {
	seen := map[string]bool{}

	out := ps[:0:0]
	for _, p := range ps {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}

	return out
}

// onDisk keeps the paths that exist.
func onDisk(ps []string) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		if _, err := os.Lstat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// buildEnv returns the only environment a command gets: the sandbox's own PATH, HOME, TMPDIR and LANG plus the allowed cache
// variables. A container keeps the image's PATH.
func buildEnv(r rules, c Command, container bool) []string {
	vars := map[string]string{}
	for _, kv := range c.Env {
		if k, v, ok := strings.Cut(kv, "="); ok && passEnv[k] {
			vars[k] = v
		}
	}

	for _, kv := range r.Env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			vars[k] = v
		}
	}

	vars["HOME"], vars["TMPDIR"] = r.Temp, r.Temp
	if _, ok := vars["LANG"]; !ok {
		vars["LANG"] = "C.UTF-8"
	}

	var env []string
	if !container {
		env = append(env, "PATH="+commandPath(r))
	}

	env = append(env, "HOME="+vars["HOME"], "TMPDIR="+vars["TMPDIR"], "LANG="+vars["LANG"])

	var rest []string
	for k, v := range vars {
		if k != "HOME" && k != "TMPDIR" && k != "LANG" {
			rest = append(rest, k+"="+v)
		}
	}

	sort.Strings(rest)
	return append(env, rest...)
}

// commandPath joins the toolchain folders in order, each followed by its bin folder when there is one, then the system
// folders.
func commandPath(r rules) string {
	// systemPath is the tail of every command's PATH.
	const systemPath = "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

	var parts []string
	for _, t := range r.Toolchains {
		parts = append(parts, t)
		if fi, err := os.Stat(filepath.Join(t, "bin")); err == nil && fi.IsDir() {
			parts = append(parts, filepath.Join(t, "bin"))
		}
	}

	return strings.Join(append(parts, systemPath), ":")
}

// seatbeltProfile renders the Seatbelt profile; later rules win, so home is closed and then reopened for the listed folders.
func seatbeltProfile(r rules, home string) string {
	var b strings.Builder
	b.WriteString("(version 1)\n(deny default)\n")
	b.WriteString("(allow process-exec process-fork sysctl-read)\n")
	b.WriteString("(allow signal (target same-sandbox))\n")
	b.WriteString("(allow mach-lookup")

	for i, n := range machServices {
		sep := " "
		if i > 0 {
			sep = "\n    "
		}
		fmt.Fprintf(&b, "%s(global-name %s)", sep, sbq(n))
	}

	b.WriteString(")\n(allow file-read*)\n")
	if home != "" {
		fmt.Fprintf(&b, "(deny file-read* (subpath %s))\n", sbq(resolved(home)))
	}

	b.WriteString("(allow file-read-metadata)\n")

	reads := append([]string{r.Worktree}, r.Readable...)
	reads = append(reads, r.Writable...)
	b.WriteString(sbRule("allow file-read*", "subpath", reads))
	b.WriteString(sbRule("deny file-read*", "literal", r.Masked))
	b.WriteString(`(allow file-write* (subpath "/dev/null") (subpath "/dev/dtracehelper"))` + "\n")

	writes := append([]string{r.Worktree}, r.Writable...)
	b.WriteString(sbRule("allow file-write*", "subpath", writes))
	b.WriteString(sbRule("deny file-write*", "subpath", r.ReadOnly))
	b.WriteString(sbRule("deny file-write*", "literal", r.Masked))
	b.WriteString(sbRule("deny file-write-unlink", "literal", maskedParents(r)))
	b.WriteString(sbRule("deny file-read* file-write*", "subpath", runFolders))
	b.WriteString(sbSockets("network-outbound", "remote", writes))
	b.WriteString(sbSockets("network-bind", "local", writes))
	if r.Network {
		b.WriteString("(allow network-outbound (remote ip))\n")
		b.WriteString(
			`(allow network-outbound (remote unix-socket ` +
				`(path-literal "/private/var/run/mDNSResponder")))` + "\n",
		)
		b.WriteString(`(allow file-read* file-write* (literal "/private/var/run/mDNSResponder"))` + "\n")
		return b.String()
	}

	b.WriteString(`(allow network-outbound (remote ip "localhost:*"))` + "\n")
	b.WriteString(`(allow network-bind network-inbound (local ip "localhost:*"))` + "\n")
	return b.String()
}

// sbq quotes a string for a Seatbelt profile.
func sbq(s string) string { return strconv.Quote(s) }

// resolved follows the symbolic links of the longest part of a path that exists, because Seatbelt matches real paths.
func resolved(p string) string {
	tail := ""
	for cur := p; ; cur = filepath.Dir(cur) {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(real, tail)
		}
		if cur == filepath.Dir(cur) {
			return p
		}
		tail = filepath.Join(filepath.Base(cur), tail)
	}
}

// sbRule renders one rule over a list of paths, resolving each through symbolic links; an empty list renders nothing.
func sbRule(action, kind string, paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	parts := make([]string, 0, len(paths))
	for _, p := range paths {
		parts = append(parts, fmt.Sprintf("(%s %s)", kind, sbq(resolved(p))))
	}
	return "(" + action + " " + strings.Join(parts, "\n    ") + ")\n"
}

// maskedParents returns every folder between the worktree and a masked file, so a masked file cannot be moved out by
// renaming its folder.
func maskedParents(r rules) []string {
	var out []string
	for _, m := range r.Masked {
		for d := filepath.Dir(m); d != r.Worktree && d != filepath.Dir(d); d = filepath.Dir(d) {
			if !within(r.Worktree, d) {
				break
			}
			out = append(out, d)
		}
	}

	return dedupe(out)
}

// sbSockets renders the rule that lets a command reach Unix sockets only inside the given folders.
func sbSockets(op, side string, dirs []string) string {
	parts := make([]string, 0, len(dirs))
	for _, d := range dirs {
		parts = append(parts, fmt.Sprintf("(subpath %s)", sbq(resolved(d))))
	}
	return fmt.Sprintf("(allow %s (%s unix-socket %s))\n", op, side, strings.Join(parts, " "))
}

// bwrapArgs renders the Bubblewrap arguments up to the working folder; the caller appends "--" and the command. varRunDir is
// true when /var/run is a folder of its own rather than a link to /run.
func bwrapArgs(r rules, home string, varRunDir bool) []string {
	a := []string{"--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/run"}
	if varRunDir {
		a = append(a, "--tmpfs", "/var/run")
	}
	if home != "" {
		a = append(a, "--tmpfs", home)
	}

	a = append(a, "--tmpfs", "/tmp", "--bind", r.Worktree, r.Worktree)
	for _, p := range r.ReadOnly {
		a = append(a, "--ro-bind", p, p)
	}

	for _, p := range r.Readable {
		a = append(a, "--ro-bind", p, p)
	}

	for _, p := range r.Writable {
		a = append(a, "--bind", p, p)
	}

	for _, p := range r.Masked {
		a = append(a, "--ro-bind", "/dev/null", p)
	}

	a = append(a, "--unshare-pid")
	if !r.Network {
		a = append(a, "--unshare-net")
	}

	return append(a, "--die-with-parent", "--new-session", "--chdir", r.Worktree)
}
