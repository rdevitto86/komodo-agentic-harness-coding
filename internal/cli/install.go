package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/format"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// The markers that fence the Komodo block inside AGENTS.md.
const (
	BlockBegin = "<!-- komodo:begin min=%s -->" // %s is the repository pin
	BlockEnd   = "<!-- komodo:end -->"
)

// The install steps that own entries.
const (
	stepBinary   = 1
	stepPath     = 2
	stepAgents   = 4
	stepClaude   = 5
	stepSettings = 6
)

// Block is the Komodo block found in a repository's AGENTS.md.
type Block struct {
	Min   string // pin from the begin marker
	Start int    // byte offset of the begin marker
	End   int    // byte offset just after the end marker
}

// Manifest is the body of install.json: everything install owns in one place.
type Manifest struct {
	Entries    []Entry    `json:"entries"`    // one per owned file or settings entry
	Selections Selections `json:"selections"` // the optional parts a person chose
}

// Selections are the optional parts chosen at install; a rerun without a flag keeps them.
type Selections struct {
	Hosts   []string `json:"hosts,omitempty"` // "claude", "codex"
	Project bool     `json:"project"`         // the --project files are owned
}

// Entry records one owned file or settings entry and the content install wrote.
type Entry struct {
	Target string `json:"target"`          // path of the file
	Entry  string `json:"entry,omitempty"` // for a settings file, which entry inside it
	Hash   string `json:"hash"`            // fingerprint of the content install wrote
	Step   int    `json:"step"`            // number of the install step that wrote it
}

// InstallData is the data of reply.install.
type InstallData struct {
	DryRun  bool            `json:"dry_run"`
	Changes []InstallChange `json:"changes"` // one row per file; with --dry-run also the files that stay
	Edited  []string        `json:"edited"`  // owned files left alone because a person changed them
	Skipped []string        `json:"skipped"` // steps that did not run, with the reason
	Probe   []InstallProbe  `json:"probe"`   // one row per model link checked
}

// InstallChange is one file install wrote, pruned or, in a dry run, would keep.
type InstallChange struct {
	File   string `json:"file"`
	Action string `json:"action"` // write, keep or prune
	Reason string `json:"reason"`
}

// InstallProbe is the result of one model link probe.
type InstallProbe struct {
	Link   string `json:"link"`
	Result string `json:"result"` // ok, or the reason the link is unusable
}

// item is one wanted target; Hash is the fingerprint of the owned content.
type item struct {
	Entry
	machine bool         // true for an entry of the machine-wide record
	apply   func() error // writes the owned content; nil for an entry only being pruned
}

// diskState is what a target holds now.
type diskState struct {
	exists bool
	hash   string // fingerprint of the owned content found on disk
}

// planRow is the decision for one target.
type planRow struct {
	it     item
	action string // write, keep or prune
	reason string
	edited bool   // kept because the owned content was changed by hand
	rec    *Entry // the recorded entry, nil when none
}

// installer holds what one install run needs.
type installer struct {
	cli                           *CLI
	host                          Host
	home, user, root, common      string
	machine                       *state.Machine
	repoOnly, machineOnly, noPath bool
	force, remove, dry            bool
	project, noProject            bool
	from                          string
	hosts                         []string
	errOut                        func(f string, a ...any)
	doMachine, doRepo             bool
	skipped                       []string
	machineRec, repoRec           []Entry
	sel, machineSel               Selections
	machineRev                    int
	hasRepoManifest               bool
}

// handleInstall installs the binary, the path entry, the provider probe and the repository files, or undoes them with --remove.
func handleInstall(c *Call) Result {
	in, res := newInstaller(c)
	if res != nil {
		return *res
	}
	return in.run()
}

// newInstaller reads the flags and the places an install touches.
func newInstaller(c *Call) (*installer, *Result) {
	in := &installer{cli: c.cli, host: c.cli.deps.Host, root: c.Repo.Root, common: c.CommonDir}
	in.repoOnly, in.machineOnly = flagBool(c, "repo-only"), flagBool(c, "machine-only")
	in.noPath, in.force, in.remove = flagBool(c, "no-path"), flagBool(c, "force"), flagBool(c, "remove")
	in.project, in.noProject = flagBool(c, "project"), flagBool(c, "no-project")
	in.from, in.dry = flagString(c, "from"), c.Global.DryRun
	in.errOut = func(f string, a ...any) { _, _ = fmt.Fprintf(c.cli.errOut, f, a...) }
	if in.repoOnly && in.machineOnly || in.project && in.noProject {
		r := usageErr("--repo-only and --machine-only cannot combine, and neither can --project and --no-project", helpFix("install"))
		return nil, &r
	}
	for _, h := range strings.Split(flagString(c, "hosts"), ",") {
		switch h = strings.TrimSpace(h); h {
		case "":
		case "claude", "codex":
			in.hosts = append(in.hosts, h)
		default:
			r := usageErr(fmt.Sprintf("%q is not a host", h), "use --hosts claude, codex or claude,codex")
			return nil, &r
		}
	}
	if in.repoOnly && in.root == "" {
		r := refuse(format.NoRepo, "no git repository found", "komodo install --repo-only inside a git repository, or pass -C <path>")
		return nil, &r
	}
	var err error
	if in.home, err = c.cli.komodoHome(); err == nil {
		in.user, err = os.UserHomeDir()
	}
	if err == nil {
		in.machine, err = c.cli.machine()
	}
	if err != nil {
		r := internalErr("find the install folders: " + err.Error())
		return nil, &r
	}
	in.doMachine = !in.repoOnly
	in.doRepo = !in.machineOnly && in.root != ""
	if !in.machineOnly && !in.repoOnly && in.root == "" {
		in.skipped = append(in.skipped, "steps 4 to 7: not inside a git repository")
	}
	return in, nil
}

// run takes the install steps: compute the wanted set, diff it, apply the differences and probe the links.
func (in *installer) run() Result {
	if err := in.load(); err != nil {
		return internalErr(err.Error())
	}
	machineWant, repoWant, err := in.wanted()
	if err != nil {
		return internalErr(err.Error())
	}
	var mrows, rrows []planRow
	if in.doMachine {
		mrows = planInstall(machineWant, in.machineRec, in.disk, in.force)
	}
	if in.doRepo {
		rrows = planInstall(repoWant, in.repoRec, in.disk, in.force)
	}
	data := InstallData{DryRun: in.dry, Changes: []InstallChange{}, Edited: []string{}, Skipped: in.skipped, Probe: []InstallProbe{}}
	if data.Skipped == nil {
		data.Skipped = []string{}
	}
	rows := append(slices.Clone(mrows), rrows...)
	for _, r := range rows {
		if r.edited {
			data.Edited = append(data.Edited, r.it.Target)
		}
	}
	data.Edited = slices.Compact(data.Edited)
	var applyErr error
	if !in.dry {
		applyErr = in.apply(rows)
	}
	mchanged, rchanged, err := in.writeManifests(mrows, rrows)
	if err != nil && applyErr == nil {
		applyErr = err
	}
	if applyErr != nil {
		return refuse(format.Internal, applyErr.Error(), "fix the file named above, then run komodo install again")
	}
	data.Changes = in.changes(rows, mchanged, rchanged)
	if !in.dry && in.doMachine && !in.remove {
		data.Probe = in.probe(context.Background())
	}
	for _, f := range data.Edited {
		in.errOut("Warning: %s was edited by hand and was left alone. Fix: komodo install --force\n", f)
	}
	if in.project {
		in.errOut("Warning: --project adds no files: no project file templates exist yet\n")
	}
	return installResult(data)
}

// installResult builds the reply from the finished data.
func installResult(d InstallData) Result {
	ch := format.Table{Head: []string{"file", "action", "reason"}, What: "files"}
	for _, c := range d.Changes {
		ch.Rows = append(ch.Rows, []string{c.File, c.Action, c.Reason})
	}
	pr := format.Table{Head: []string{"link", "result"}, What: "links"}
	for _, p := range d.Probe {
		pr.Rows = append(pr.Rows, []string{p.Link, p.Result})
	}
	return Result{Shape: "reply.install", Facts: format.InstallFacts{Changes: ch, Probe: pr}, Data: d}
}

// load reads the recorded manifests and the selections.
func (in *installer) load() error {
	if in.doMachine {
		rec, err := state.LoadMachine[state.InstallBody](in.machine, state.KindInstall)
		if err != nil && !errors.Is(err, state.ErrNotFound) {
			return fmt.Errorf("read install.json: %w", err)
		}
		in.machineRev = rec.Header.Rev
		for _, e := range rec.Body.Entries {
			in.machineRec = append(in.machineRec, Entry(e))
		}
		in.machineSel = Selections{Hosts: rec.Body.Selections.Hosts, Project: rec.Body.Selections.Project}
		in.sel = in.machineSel
	}
	if in.doRepo {
		m, ok, err := readRepoManifest(in.repoManifestPath())
		if err != nil {
			return err
		}
		in.hasRepoManifest = ok
		if ok {
			in.repoRec, in.sel = m.Entries, m.Selections
		}
	}
	if len(in.hosts) > 0 {
		in.sel.Hosts = in.hosts
	}
	if in.project {
		in.sel.Project = true
	}
	if in.noProject {
		in.sel.Project = false
	}
	return nil
}

// repoManifestPath returns the repository manifest in the git common directory.
func (in *installer) repoManifestPath() string {
	return filepath.Join(in.common, "komodo", "install.json")
}

// readRepoManifest reads the repository manifest; an absent file is an empty manifest.
func readRepoManifest(path string) (Manifest, bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Manifest{}, false, nil
	}
	if err != nil {
		return Manifest{}, false, fmt.Errorf("read install.json: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Manifest{}, false, fmt.Errorf("read %s: %w", path, err)
	}
	return m, true, nil
}

// planInstall diffs the wanted targets against the recorded entries and decides write, keep or prune for each.
func planInstall(wanted []item, recorded []Entry, disk func(machine bool, e Entry) diskState, force bool) []planRow {
	var rows []planRow
	seen := map[string]bool{}
	recOf := func(e Entry) *Entry {
		for i := range recorded {
			if recorded[i].Target == e.Target && recorded[i].Entry == e.Entry {
				return &recorded[i]
			}
		}
		return nil
	}
	for _, w := range wanted {
		rec := recOf(w.Entry)
		seen[w.Target+"\x00"+w.Entry.Entry] = true
		d := disk(w.machine, w.Entry)
		row := planRow{it: w, rec: rec}
		switch {
		case rec == nil && d.exists && d.hash == w.Hash:
			row.action, row.reason = "keep", "already current"
		case rec == nil && d.exists:
			row.action, row.reason = "write", "not yet owned"
		case rec == nil:
			row.action, row.reason = "write", "missing"
		case !d.exists:
			row.action, row.reason = "write", "missing"
		case d.hash != rec.Hash && !force:
			row.action, row.reason, row.edited = "keep", "hand edit", true
		case d.hash != rec.Hash:
			row.action, row.reason = "write", "hand edit overwritten"
		case w.Hash == rec.Hash:
			row.action, row.reason = "keep", "unchanged"
		default:
			row.action, row.reason = "write", "content changed"
		}
		rows = append(rows, row)
	}
	for i := range recorded {
		rec := &recorded[i]
		if seen[rec.Target+"\x00"+rec.Entry] {
			continue
		}
		machine := filepath.IsAbs(rec.Target)
		it := item{Entry: *rec, machine: machine}
		d := disk(machine, *rec)
		row := planRow{it: it, rec: rec}
		switch {
		case !d.exists:
			row.action, row.reason = "prune", "already gone"
		case d.hash != rec.Hash && !force:
			row.action, row.reason, row.edited = "keep", "hand edit", true
		case d.hash != rec.Hash:
			row.action, row.reason = "prune", "hand edit removed"
		default:
			row.action, row.reason = "prune", "no longer wanted"
		}
		rows = append(rows, row)
	}
	slices.SortStableFunc(rows, func(a, b planRow) int { return a.it.Step - b.it.Step })
	return rows
}

// apply takes the write and prune actions in step order and stops at the first failure.
func (in *installer) apply(rows []planRow) error {
	for i := range rows {
		r := &rows[i]
		var err error
		switch {
		case r.action == "write" && r.it.apply != nil:
			err = r.it.apply()
		case r.action == "prune" && r.reason != "already gone":
			err = in.prune(r.it.machine, r.it.Entry)
		}
		if err != nil {
			for j := i; j < len(rows); j++ {
				rows[j].action, rows[j].reason = "failed", "not applied"
			}
			return err
		}
	}
	return nil
}

// writeManifests records the applied entries in the repository manifest and the machine-wide record.
func (in *installer) writeManifests(mrows, rrows []planRow) (machineChanged, repoChanged bool, err error) {
	if in.doMachine {
		body := state.InstallBody{Entries: []state.InstallEntry{}, Selections: state.InstallSelections{Hosts: in.sel.Hosts, Project: in.sel.Project}}
		for _, e := range recordedAfter(mrows) {
			body.Entries = append(body.Entries, state.InstallEntry(e))
		}
		machineChanged = !in.manifestSame(body)
		if machineChanged && !in.dry {
			if _, err = updateMachine(in.machine, state.KindInstall, func(b *state.InstallBody) bool { *b = body; return true }); err != nil {
				return false, false, fmt.Errorf("write install.json: %w", err)
			}
		}
	}
	if in.doRepo {
		m := Manifest{Entries: recordedAfter(rrows), Selections: in.sel}
		if m.Selections.Hosts == nil {
			m.Selections.Hosts = []string{}
		}
		old, _, _ := readRepoManifest(in.repoManifestPath())
		repoChanged = !in.hasRepoManifest || !reflect.DeepEqual(old.Entries, m.Entries) || !sameSelections(old.Selections, m.Selections)
		if repoChanged && !in.dry {
			b, _ := json.MarshalIndent(m, "", "  ")
			if err = os.MkdirAll(filepath.Dir(in.repoManifestPath()), 0o755); err == nil {
				err = core.WriteFileAtomic(in.repoManifestPath(), append(b, '\n'), 0o644)
			}
			if err != nil {
				return machineChanged, false, fmt.Errorf("write %s: %w", in.repoManifestPath(), err)
			}
		}
	}
	return machineChanged, repoChanged, nil
}

// sameSelections compares two selections, treating no hosts and an empty list alike.
func sameSelections(a, b Selections) bool {
	return a.Project == b.Project && slices.Equal(a.Hosts, b.Hosts)
}

// manifestSame reports whether the recorded machine-wide body already equals body.
func (in *installer) manifestSame(body state.InstallBody) bool {
	if in.machineRev == 0 {
		return false
	}
	old := make([]state.InstallEntry, 0, len(in.machineRec))
	for _, e := range in.machineRec {
		old = append(old, state.InstallEntry(e))
	}
	return reflect.DeepEqual(old, body.Entries) &&
		sameSelections(in.machineSel, Selections{Hosts: body.Selections.Hosts, Project: body.Selections.Project})
}

// recordedAfter returns the entries the manifest holds once the rows are applied, sorted by step.
func recordedAfter(rows []planRow) []Entry {
	out := []Entry{}
	for _, r := range rows {
		switch {
		case r.action == "prune":
		case r.action == "failed" && r.rec == nil:
		case r.action == "failed" || (r.action == "keep" && r.rec != nil):
			out = append(out, *r.rec)
		default:
			out = append(out, r.it.Entry)
		}
	}
	slices.SortStableFunc(out, func(a, b Entry) int {
		if a.Step != b.Step {
			return a.Step - b.Step
		}
		return strings.Compare(a.Target+a.Entry, b.Target+b.Entry)
	})
	return out
}

// changes folds the rows into one line per file, adds the manifest files, and drops what stayed unless this is a dry run.
func (in *installer) changes(rows []planRow, machineChanged, repoChanged bool) []InstallChange {
	var out []InstallChange
	idx := map[string]int{}
	rank := map[string]int{"keep": 0, "prune": 1, "write": 2, "failed": 3}
	for _, r := range rows {
		file := r.it.Target
		i, ok := idx[file]
		if !ok {
			idx[file] = len(out)
			out = append(out, InstallChange{File: file, Action: r.action, Reason: r.reason})
			continue
		}
		c := &out[i]
		if rank[r.action] > rank[c.Action] {
			c.Action = r.action
		}
		if !strings.Contains(c.Reason, r.reason) {
			c.Reason += "; " + r.reason
		}
	}
	for _, m := range []struct {
		on      bool
		changed bool
		file    string
	}{
		{in.doMachine, machineChanged, filepath.Join(in.home, "state", "install.json")},
		{in.doRepo, repoChanged, relTo(in.root, in.repoManifestPath())},
	} {
		if !m.on {
			continue
		}
		action, reason := "keep", "unchanged"
		if m.changed {
			action, reason = "write", "manifest changed"
		}
		out = append(out, InstallChange{File: m.file, Action: action, Reason: reason})
	}
	if in.dry {
		return out
	}
	kept := out[:0]
	for _, c := range out {
		if c.Action != "keep" {
			kept = append(kept, c)
		}
	}
	return slices.Clone(kept)
}

// relTo returns path relative to base, or path when it lies elsewhere.
func relTo(base, path string) string {
	if r, err := filepath.Rel(base, path); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return path
}

// abs returns the absolute path of a recorded target.
func (in *installer) abs(target string) string {
	if filepath.IsAbs(target) {
		return target
	}
	return filepath.Join(in.root, target)
}

// sha returns the lowercase hexadecimal SHA-256 of b.
func sha(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// updateMachine reads a machine-wide record, lets mutate change its body and saves it with a revision check, rereading on a conflict.
func updateMachine[T any](m *state.Machine, k state.MachineKind, mutate func(body *T) bool) (bool, error) {
	for range 3 {
		rec, err := state.LoadMachine[T](m, k)
		if err != nil && !errors.Is(err, state.ErrNotFound) {
			return false, err
		}
		if !mutate(&rec.Body) {
			return false, nil
		}
		_, err = state.SaveMachine(m, k, rec.Body, rec.Header.Rev)
		if !errors.Is(err, state.ErrStaleRev) && !errors.Is(err, state.ErrExists) {
			return err == nil, err
		}
	}
	return false, state.ErrStaleRev
}

// probe asks the model client to probe every link and stores each result in providers.json.
func (in *installer) probe(ctx context.Context) []InstallProbe {
	rows, _, err := probeLinks(ctx, in.host, in.machine)
	if err != nil {
		in.errOut("Warning: record the model link probe: %v\n", err)
	}
	return rows
}
