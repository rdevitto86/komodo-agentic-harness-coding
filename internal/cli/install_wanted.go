package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/format"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// pathMarker ends the one line install adds to a shell start-up file.
const pathMarker = " # komodo:path"

// wanted builds the targets each step would write; with --remove nothing is wanted.
func (in *installer) wanted() (machine, repo []item, err error) {
	if in.remove {
		return nil, nil, nil
	}
	if in.doMachine {
		if machine, err = in.binaryItems(); err != nil {
			return nil, nil, err
		}
		machine = append(machine, in.pathItems()...)
	}
	if in.doRepo {
		blk, err := in.blockItem()
		if err != nil {
			return nil, nil, err
		}
		repo = append(repo, blk)
		if slices.Contains(in.hostsWanted(), "claude") {
			repo = append(repo, in.claudeItem())
			repo = append(repo, in.settingsItems()...)
		}
	}
	return machine, repo, nil
}

// hostsWanted returns the chosen hosts, else the hosts found on this machine.
func (in *installer) hostsWanted() []string {
	if len(in.sel.Hosts) > 0 {
		return in.sel.Hosts
	}

	var out []string
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }

	if exists(filepath.Join(in.root, "CLAUDE.md")) || exists(filepath.Join(in.root, ".claude")) || exists(filepath.Join(in.user, ".claude")) {
		out = append(out, "claude")
	} else if _, err := in.host.lookPath("claude"); err == nil {
		out = append(out, "claude")
	}
	if exists(filepath.Join(in.root, ".codex")) || exists(filepath.Join(in.user, ".codex")) {
		out = append(out, "codex")
	} else if _, err := in.host.lookPath("codex"); err == nil {
		out = append(out, "codex")
	}
	return out
}

// binaryItems wants the binary copied into versions/<v>/ and onto bin/komodo.
func (in *installer) binaryItems() ([]item, error) {
	src, version := in.from, core.BinaryVersion()
	if src != "" {
		v, err := binaryVersion(context.Background(), src)
		if err != nil {
			return nil, err
		}
		version = v
	} else {
		var err error
		if src, err = in.host.executable(); err != nil {
			return nil, fmt.Errorf("find the running binary: %w", err)
		}
	}

	data, err := os.ReadFile(src)
	if err != nil {
		return nil, fmt.Errorf("read the binary: %w", err)
	}

	h := sha(data)
	write := func(p string) func() error {
		return func() error {
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return fmt.Errorf("create folder of %s: %w", p, err)
			}
			return core.WriteFileAtomic(p, data, 0o755)
		}
	}

	ver := filepath.Join(in.home, "versions", version, "komodo")
	bin := filepath.Join(in.home, "bin", "komodo")

	return []item{
		{Entry: Entry{Target: ver, Hash: h, Step: stepBinary}, machine: true, apply: write(ver)},
		{Entry: Entry{Target: bin, Hash: h, Step: stepBinary}, machine: true, apply: write(bin)},
	}, nil
}

// binaryVersion asks a binary for its version, which it prints alone or in the reply envelope.
func binaryVersion(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("ask %s for its version: %w", path, err)
	}

	s := strings.TrimSpace(string(out))
	if strings.HasPrefix(s, "{") {
		var env struct {
			Data struct {
				Version string `json:"version"`
			} `json:"data"`
		}
		if json.Unmarshal(out, &env) == nil {
			s = env.Data.Version
		}
	}

	if _, err := core.ParseSemVer(s); err != nil {
		return "", fmt.Errorf("%s printed %q, which is not a version", path, s)
	}
	return s, nil
}

// pathItems wants komodo on PATH: a symlink into ~/.local/bin when that is on PATH, else one marked line in a shell file.
// An entry recorded earlier is kept, so a rerun that finds komodo on PATH does not prune its own work.
func (in *installer) pathItems() []item {
	bin := filepath.Join(in.home, "bin", "komodo")
	var recorded *Entry
	for i := range in.machineRec {
		if in.machineRec[i].Step == stepPath {
			recorded = &in.machineRec[i]
		}
	}

	if recorded == nil {
		if in.noPath {
			return nil
		}
		if _, err := in.host.lookPath("komodo"); err == nil {
			return nil
		}
	}

	link := filepath.Join(in.user, ".local", "bin", "komodo")
	if recorded != nil && recorded.Target != link && recorded.Target != "" && filepath.Base(recorded.Target) == "komodo" {
		link = recorded.Target
	}

	useLink := (recorded != nil && recorded.Target == link) || (recorded == nil && onPath(filepath.Dir(link)))
	if useLink {
		return []item{{
			Entry: Entry{Target: link, Hash: sha([]byte(bin)), Step: stepPath}, machine: true,
			apply: func() error { return putSymlink(link, bin) },
		}}
	}

	rc := filepath.Join(in.user, rcName(os.Getenv("SHELL")))
	if recorded != nil {
		rc = recorded.Target
	}

	line := rcLine(filepath.Dir(bin))
	return []item{{
		Entry: Entry{Target: rc, Hash: sha([]byte(line)), Step: stepPath}, machine: true,
		apply: func() error { return putRcLine(rc, line) },
	}}
}

// rcName names the shell start-up file for a $SHELL value.
func rcName(shell string) string {
	switch filepath.Base(shell) {
	case "zsh":
		return ".zshrc"
	case "bash":
		return ".bashrc"
	}
	return ".profile"
}

// rcLine is the one marked line that puts dir on PATH.
func rcLine(dir string) string { return `export PATH="` + dir + `:$PATH"` + pathMarker }

// onPath reports whether dir is one of the folders on PATH.
func onPath(dir string) bool {
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.Clean(p) == filepath.Clean(dir) {
			return true
		}
	}
	return false
}

// putSymlink points link at dest, replacing an older symlink and refusing to replace a regular file.
func putSymlink(link, dest string) error {
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return fmt.Errorf("create folder of %s: %w", link, err)
	}
	if fi, err := os.Lstat(link); err == nil {
		if fi.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("%s exists and is not a symlink: move it away, then run komodo install again", link)
		}
		if err := os.Remove(link); err != nil {
			return fmt.Errorf("replace %s: %w", link, err)
		}
	}
	if err := os.Symlink(dest, link); err != nil {
		return fmt.Errorf("link %s: %w", link, err)
	}
	return nil
}

// putRcLine adds the marked line to a shell file, or replaces the one already there.
func putRcLine(path, line string) error {
	text, _, err := readText(path)
	if err != nil {
		return err
	}

	var out []string
	done := false

	for _, l := range strings.Split(text, "\n") {
		if strings.HasSuffix(strings.TrimSpace(l), pathMarker) {
			if !done {
				out = append(out, line)
			}
			done = true
			continue
		}
		out = append(out, l)
	}

	text = strings.Join(out, "\n")
	if !done {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += line + "\n"
	}
	return writeText(path, text)
}

// dropRcLine removes the marked line from a shell file.
func dropRcLine(path string) error {
	text, ok, err := readText(path)
	if err != nil || !ok {
		return err
	}

	var keep []string
	for _, l := range strings.SplitAfter(text, "\n") {
		if !strings.HasSuffix(strings.TrimSpace(l), pathMarker) {
			keep = append(keep, l)
		}
	}
	return writeText(path, strings.Join(keep, ""))
}

// claudeItem wants the import line in CLAUDE.md.
func (in *installer) claudeItem() item {
	return item{
		Entry: Entry{Target: "CLAUDE.md", Hash: sha([]byte(importLine)), Step: stepClaude},
		apply: func() error {
			p := filepath.Join(in.root, "CLAUDE.md")
			text, _, err := readText(p)
			if err != nil {
				return err
			}
			return writeText(p, addImport(text))
		},
	}
}

// settingsItems wants each entry of .claude/settings.json.
func (in *installer) settingsItems() []item {
	var out []item
	for _, e := range settingsEntries {
		out = append(out, item{
			Entry: Entry{
				Target: filepath.Join(".claude", "settings.json"),
				Entry:  e.id,
				Hash:   sha([]byte(e.value)),
				Step:   stepSettings,
			},
			apply: func() error {
				return putSettings(filepath.Join(in.root, ".claude", "settings.json"), e.id)
			},
		})
	}
	return out
}

// blockItem wants the Komodo block in AGENTS.md, with the pin that only rises.
func (in *installer) blockItem() (item, error) {
	path := filepath.Join(in.root, "AGENTS.md")
	text, _, err := readText(path)
	if err != nil {
		return item{}, fmt.Errorf("read AGENTS.md: %w", err)
	}

	existing := ""
	if b, ok, ferr := findBlock(text); ferr == nil && ok {
		existing = b.Min
	}

	seen := ""
	if rec, err := state.LoadMachine[state.UpdateBody](in.machine, state.KindUpdate); err == nil {
		seen = rec.Body.Latest
	}

	pin, err := nextPin(existing, core.BinaryVersion(), seen)
	if err != nil {
		return item{}, err
	}

	var rules string
	if in.host.Style != nil {
		if rules, err = in.host.Style(); err != nil {
			return item{}, fmt.Errorf("read the orchestrator rules: %w", err)
		}
	}

	style, err := format.OutputStyle(in.profile())
	if err != nil {
		return item{}, err
	}

	block := renderBlock(pin, filepath.Join(in.home, "bin", "komodo"), rules, style)

	return item{
		Entry: Entry{Target: "AGENTS.md", Hash: sha([]byte(block)), Step: stepAgents},
		apply: func() error {
			cur, _, err := readText(path)
			if err != nil {
				return err
			}
			next, err := putBlock(cur, block)
			if err != nil {
				return err
			}
			return writeText(path, next)
		},
	}, nil
}

// profile returns the output profile the Komodo block carries the style of.
func (in *installer) profile() format.Profile {
	if cfg := in.cli.deps.Config; cfg != nil {
		if it, err := cfg.GetConfigItem("output.profile"); err == nil && it.Value == string(format.ProfileStandard) {
			return format.ProfileStandard
		}
	}
	return format.ProfileAdhd
}

// nextPin returns the pin to write: the running version without its build suffix,
// capped at the newest release seen, and never below the existing pin.
func nextPin(existing, running, seen string) (string, error) {
	r, err := core.ParseSemVer(running)
	if err != nil {
		return "", fmt.Errorf("running version: %w", err)
	}
	r.Build = nil
	if s, err := core.ParseSemVer(seen); err == nil && r.Compare(s) > 0 {
		r = s
		r.Build = nil
	}
	if e, err := core.ParseSemVer(existing); err == nil && e.Compare(r) >= 0 {
		return existing, nil
	}
	return r.String(), nil
}

// disk reads what a target holds now, in the form its fingerprint covers.
func (in *installer) disk(_ bool, e Entry) diskState {
	p := in.abs(e.Target)
	switch e.Step {
	case stepBinary:
		b, err := os.ReadFile(p)
		return diskState{exists: err == nil, hash: sha(b)}
	case stepPath:
		return pathState(p)
	case stepAgents:
		h, ok := blockHash(p)
		return diskState{exists: ok, hash: h}
	case stepClaude:
		text, _, _ := readText(p)
		if hasImport(text) {
			return diskState{exists: true, hash: sha([]byte(importLine))}
		}
	case stepSettings:
		return settingsState(p, e.Entry)
	}
	return diskState{}
}

// pathState reads the path entry: a symlink by where it points, a shell file by its marked line.
func pathState(p string) diskState {
	fi, err := os.Lstat(p)
	if err != nil {
		return diskState{}
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		dest, _ := os.Readlink(p)
		return diskState{exists: true, hash: sha([]byte(dest))}
	}

	text, _, _ := readText(p)
	for _, l := range strings.Split(text, "\n") {
		if t := strings.TrimSpace(l); strings.HasSuffix(t, pathMarker) {
			return diskState{exists: true, hash: sha([]byte(t))}
		}
	}
	return diskState{hash: sha(nil)}
}

// prune removes the owned part of a target and leaves everything else in the file.
func (in *installer) prune(_ bool, e Entry) error {
	p := in.abs(e.Target)

	switch e.Step {
	case stepBinary:
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if filepath.Base(filepath.Dir(filepath.Dir(p))) == "versions" {
			_ = os.Remove(filepath.Dir(p))
		}
	case stepPath:
		if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return os.Remove(p)
		}
		return dropRcLine(p)
	case stepAgents:
		return in.dropFromFile(p, dropBlock)
	case stepClaude:
		return in.dropFromFile(p, func(s string) (string, error) { return dropImport(s), nil })
	case stepSettings:
		return dropSettings(p, e.Entry)
	}
	return nil
}

// dropFromFile rewrites a file without the owned part, and removes the file when nothing else is left.
func (in *installer) dropFromFile(path string, drop func(string) (string, error)) error {
	text, ok, err := readText(path)
	if err != nil || !ok {
		return err
	}

	next, err := drop(text)
	if err != nil {
		return err
	}
	if strings.TrimSpace(next) == "" {
		return os.Remove(path)
	}
	return writeText(path, next)
}

// probeLinks probes every model link and stores each result in providers.json, leaving the other fields of a link alone.
func probeLinks(ctx context.Context, host Host, m *state.Machine) ([]InstallProbe, []llm.ProbeResult, error) {
	rows := []InstallProbe{}
	if host.Links == nil {
		return rows, nil, nil
	}

	results := host.Links.ProbeAll(ctx)
	for _, r := range results {
		res := "ok"
		if !r.OK {
			if res = r.Reason; res == "" {
				res = "unavailable"
			}
		}
		rows = append(rows, InstallProbe{Link: linkName(r.Link), Result: res})
	}

	_, err := updateMachine(m, state.KindProviders, func(b *state.ProvidersBody) bool {
		if b.Links == nil {
			b.Links = map[string]state.LinkRecord{}
		}
		for _, r := range results {
			rec := b.Links[linkName(r.Link)]
			rec.Probe, _ = json.Marshal(r)
			b.Links[linkName(r.Link)] = rec
		}
		return true
	})
	return rows, results, err
}

// linkName spells a link as providers.json keys it.
func linkName(l llm.Link) string { return string(l) }
