package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/contextmgr"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	komodo "github.com/rdevitto86/komodo-agentic-harness-coding"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cache"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/commit"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/config"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/intake"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

// groupRef lets the Commit and Cleanup handlers reach the Task Manager that is built after them.
type groupRef struct {
	mu        sync.Mutex
	tm        *task.Manager
	recheckMu sync.Mutex // serialises rechecks so each reads its own verdict
	slot      recheckSlot
}

// CloseTaskGroup closes the group's Task Manager, which ends its sandbox and the group.
func (g *groupRef) CloseTaskGroup(ctx context.Context) error {
	g.mu.Lock()
	tm := g.tm
	g.mu.Unlock()
	if tm == nil {
		return nil
	}
	if err := tm.CloseTaskGroup(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}

	return nil
}

// manager returns the group's Task Manager, or nil before it is built.
func (g *groupRef) manager() *task.Manager {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.tm
}

// landingCommitter fills the approval with the landing plan the Runtime Manager holds at publish time.
type landingCommitter struct {
	h    *commit.Handler
	k    *repoKit
	spec engine.GroupSpec
}

// Save makes a local commit of one finished task.
func (l landingCommitter) Save(ctx context.Context, r commit.SaveRequest) (commit.SaveResult, error) {
	r.Group = bus.ID(l.spec.ID)
	return l.h.Save(ctx, r)
}

// Publish lands the approved group using the epic's current landing order.
func (l landingCommitter) Publish(ctx context.Context, a commit.Approval) (commit.Result, error) {
	a.Group = bus.ID(l.spec.ID)
	a.EpicGroups = l.k.epicGroups(l.spec)
	return l.h.Publish(ctx, a)
}

// commitReporter relays progress facts of the Commit Handler.
type commitReporter struct{ s *stack }

// Report relays a progress fact to the Runtime Manager.
func (r commitReporter) Report(ctx context.Context, group bus.ID, subtype string, data any) error {
	return reportFact(ctx, r.s.mgr, string(group), subtype, data)
}

// reporter relays progress facts of the Cleanup Handler.
type reporter struct{ s *stack }

// Report relays a progress fact to the Runtime Manager.
func (r reporter) Report(ctx context.Context, group, subtype string, data any) error {
	return reportFact(ctx, r.s.mgr, group, subtype, data)
}

// gitRunner runs the host git in a repository for the base tree view.
type gitRunner struct{ dir string }

// Run runs git with the arguments in the repository.
func (g gitRunner) Run(ctx context.Context, rest ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", g.dir}, rest...)...).Output()
	return strings.TrimSpace(string(out)), err
}

// limits adapts the machine records to the Task Manager's limit store by link name.
type limits struct{ m *state.Machine }

// markerLanguage names the language a marker file stands for; the checks use it to pick their impact set.
var markerLanguage = map[string]string{
	"go.mod":           "go",
	"Cargo.toml":       "rust",
	"package.json":     "typescript",
	"pyproject.toml":   "python",
	"requirements.txt": "python",
}

// ResetAt returns when the link's limit lifts.
func (l limits) ResetAt(k llm.Link) (time.Time, bool) { return l.m.ResetAt(string(k)) }

// Record stores a link's limit.
func (l limits) Record(k llm.Link, reset time.Time, c llm.CapKind) error {
	return l.m.Record(string(k), reset, string(c))
}

// TakeSlot counts one more live session on the link.
func (l limits) TakeSlot(k llm.Link, most int) (bool, error) {
	return l.m.TakeSlot(string(k), most)
}

// ReleaseSlot counts one session fewer on the link.
func (l limits) ReleaseSlot(k llm.Link) error { return l.m.ReleaseSlot(string(k)) }

// Start builds the group's Task Manager, serves it on a goroutine and returns the engine's end of its channels.
func (k *repoKit) start(ctx context.Context, spec engine.GroupSpec, h cache.Handle) (engine.Channels, error) {
	s := k.s

	epics, _, err := intake.Load(k.root)
	if err != nil {
		return engine.Channels{}, fmt.Errorf("read the task lists: %w", err)
	}

	grp, ok := findGroup(epics, spec.ID)
	if !ok {
		return engine.Channels{}, fmt.Errorf("group %s is not in the task lists", spec.Label)
	}

	epicBranch := "komodo/" + spec.Epic
	base, err := gitOut("-C", k.root, "rev-parse", epicBranch)
	if err != nil {
		return engine.Channels{}, fmt.Errorf("find the tip of %s: %w", epicBranch, err)
	}

	profiles, err := agent.LoadToolProfiles(komodo.Embedded)
	if err != nil {
		return engine.Channels{}, err
	}

	baseTree := task.NewBaseTree(gitRunner{k.root}, base)
	cmds, lang := commandTable(contextmgr.Detect(baseTree))
	lang, tools := resolveTools(profiles, baseTree, cmds, lang)
	tspec, items := k.taskSpec(spec, grp, cmds)
	gc := &task.GitCLI{Repo: k.root, Start: epicBranch, Base: base}
	ref := s.refFor(spec.ID)
	router := &task.Router{
		Client:   s.llm,
		Quota:    limits{s.machine},
		Paid:     s.str("providers.paid", "never"),
		CtxFloor: int(s.num("providers.ctx_floor")),
		Catalog:  s.catalog,
	}
	exe, _ := os.Executable()
	c := task.Collaborators{
		Git:       gc,
		Commit:    k.committer(ref, spec),
		Router:    router,
		Client:    s.llm,
		Sandboxes: s.boxes,
		Limits:    limits{s.machine},
		Cache:     h, Commands: cmds,
		Base: base, Language: lang, Exe: exe,
		Socket: filepath.Join(s.home, "state", engine.SocketName),
		Tools:  s.tools,
		Meta: task.GroupMeta{
			Repo:       spec.Repo,
			Epic:       spec.Epic,
			EpicGroups: k.epicGroups(spec),
			Goal:       grp.Goal,
		},
		Plan:        s.sandboxPlan(spec.ID),
		OnRecheck:   ref.slot.deliver,
		SessionsDir: filepath.Join(s.home, "sessions", spec.ID),
	}

	c.Briefs = &task.ContextBriefer{
		Manager: k.cm,
		Tree:    task.NewBaseTree(gitRunner{k.root}, base),
		Run:     spec.Run,
		Group:   spec.ID,
	}
	for _, d := range []string{c.Plan.Temp, c.Plan.Build, c.SessionsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return engine.Channels{}, fmt.Errorf("create %s: %w", d, err)
		}
	}

	var m *task.Manager
	secrets, sibling, err := task.CheckerPolicy(
		func() []string { return m.Files() },
		tspec.Lease.Worktree,
	)
	if err != nil {
		return engine.Channels{}, err
	}

	cc := agent.CheckerConfig{
		TestCommands: map[string]string{lang: "test"}, SecretPatterns: secrets, Sibling: sibling,
		Diff: func(ctx context.Context, base, head string) (string, error) {
			return gitRunner{tspec.Lease.Worktree}.Run(ctx, "diff", base)
		},
	}
	if tools.JUnit != "" {
		cc.ReportCommands = map[string]string{lang: tools.JUnit}
	}
	if tools.Coverage != "" {
		cc.CoverageCommands = map[string]string{lang: tools.Coverage}
	}
	if k.st != nil {
		cc.BaseResult = agent.NewBaselineSource(agent.BaselineConfig{
			Store: baselineStore{st: k.st, group: spec.ID},
			Tree: func(_ context.Context, base string) (string, error) {
				return gitOut("-C", k.root, "rev-parse", base+"^{tree}")
			},
			Checkout: func(ctx context.Context, base string) (string, error) {
				return checkoutBase(ctx, k.root, base, c.Plan.Build)
			},
			Language: lang,
			Test:     tools.Test,
			JUnit:    tools.JUnit,
		})
	}

	c.Checks = agent.NewChecker(cc)

	roles, err := task.NewRoles(task.RoleConfig{
		Group:    tspec.Group,
		Tasks:    tspec.Tasks,
		Items:    items,
		Worktree: tspec.Lease.Worktree,
		Files:    func() []string { return m.Files() },
		Runner:   func() agent.CommandRunner { return m.Runner("tester") },
	})
	if err != nil {
		return engine.Channels{}, err
	}

	c.Roles = roles
	if m, err = task.New(tspec, c); err != nil {
		return engine.Channels{}, err
	}

	ref.mu.Lock()
	ref.tm = m
	ref.mu.Unlock()

	cmdCh := make(chan task.Command, engine.CommandBuffer)
	repCh := make(chan task.Message, engine.CommandBuffer)
	inner := make(chan task.Message, engine.CommandBuffer)
	k.s.spawn(func() { _ = m.Serve(ctx, task.Channels{Commands: cmdCh, Replies: inner}) })
	k.s.spawn(func() { k.s.relay(spec.Label, inner, repCh) })
	return engine.Channels{Commands: cmdCh, Replies: repCh}, nil
}

// taskSpec converts the engine's group spec and the task list into the Task Manager's spec, its acceptance items and its
// command table.
func (k *repoKit) taskSpec(spec engine.GroupSpec, g intake.Group, cmds map[string][]string) (task.GroupSpec, []agent.Item) {
	var tasks []task.TaskSpec
	for _, t := range g.Tasks {
		class := "task:" + t.Name
		cmds[class] = []string{"sh", "-c", t.DoneWhen}
		tasks = append(tasks, task.TaskSpec{ID: t.Name, Title: t.Name, DoneWhen: class})
	}

	var items []agent.Item
	for i, a := range g.Acceptance {
		items = append(items, agent.Item{ID: fmt.Sprintf("acc-%d", i+1), Text: a.Text, Cmd: a.Cmd, Needs: a.Needs})
	}

	perms := map[string]task.Permissions{}

	for _, name := range []string{"builder", "tester", "reviewer"} {
		a, _ := k.s.cfg.GetAgent(name)
		perms[name] = permissionsOf(a)
	}

	l := spec.Limits

	ts := task.GroupSpec{
		Run:        spec.Run,
		Group:      spec.Label,
		Tasks:      tasks,
		Files:      slices.Clone(spec.Files),
		OtherFiles: spec.OtherFiles,
		Agents:     perms,
		Lease: task.LeaseRef{
			Branch:   spec.Lease.Branch,
			Worktree: spec.Lease.Worktree,
			GitDir:   filepath.Join(k.common, "worktrees", filepath.Base(spec.Lease.Worktree)),
			Fence:    int64(spec.Lease.Fence),
		},
		Limits: task.Limits{
			Returns:        l.Returns,
			Attempts:       l.GroupAttempts,
			SessionWall:    time.Duration(k.s.num("limits.session_wall_min")) * time.Minute,
			SessionIdle:    time.Duration(k.s.num("limits.session_idle_min")) * time.Minute,
			SessionRetries: l.BuilderRetries,
		},
	}
	if spec.Saved != nil {
		ts.Saved = progressOf(*spec.Saved)
	}

	return ts, items
}

// committer returns the Commit Handler of one group: the shared handler with the group's Task Manager as the closer.
func (k *repoKit) committer(ref *groupRef, spec engine.GroupSpec) task.Committer {
	h := *k.commit
	cl := *k.cleanup
	cl.Task = ref
	h.Cleanup = publishedAdapter{h: &cl, landing: k.landingPath}
	h.Check = groupChecker{ref: ref, worktree: spec.Lease.Worktree}
	return landingCommitter{h: &h, k: k, spec: spec}
}

// epicGroups returns the labels of the epic's groups in landing order.
func (k *repoKit) epicGroups(spec engine.GroupSpec) []string {
	if k.s.mgr != nil {
		if p, ok := k.s.mgr.LandingPlan(spec.ID); ok {
			return p.Order
		}
	}
	return []string{spec.Label}
}

// str reads a string setting.
func (s *stack) str(key, def string) string {
	it, err := s.cfg.GetConfigItem(key)
	if err != nil {
		return def
	}
	if v, ok := it.Value.(string); ok {
		return v
	}
	return def
}

// sandboxPlan names the folders a group's sandbox instances use: private temp and build folders, and the Go toolchain and
// module cache read-only.
func (s *stack) sandboxPlan(group string) task.SandboxPlan {
	p := task.SandboxPlan{Temp: filepath.Join(
		s.home,
		"tmp",
		group,
	), Build: filepath.Join(s.home, "build", group),
		MaxProcs: int(s.num("sandbox.max_procs")), MaxMemGB: int(s.num("sandbox.max_mem_gb"))}
	if out, err := exec.Command("go", "env", "GOROOT", "GOMODCACHE").Output(); err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) == 2 {
			p.Toolchains, p.Modules = []string{lines[0]}, lines[1]
		}
	}

	return p
}

// num reads a numeric setting.
func (s *stack) num(key string) float64 {
	it, err := s.cfg.GetConfigItem(key)
	if err != nil {
		return 0
	}

	switch v := it.Value.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	}

	return 0
}

// relay passes a Task Manager's messages to the Runtime Manager and logs escalations and endings to the engine log.
func (s *stack) relay(label string, in <-chan task.Message, out chan<- task.Message) {
	defer close(out)

	for m := range in {
		switch {
		case m.Escalation != nil:
			s.logf(
				"komodo engine: %s escalated %s %s %s\n",
				label,
				m.Escalation.Kind,
				m.Escalation.Reason,
				m.Escalation.Detail,
			)
		case m.Ended != nil:
			s.logf("komodo engine: %s ended %s %s\n", label, m.Ended.Result, m.Ended.Error)
		}

		out <- m
	}
}

// findGroup returns the group with an identifier from the task lists.
func findGroup(epics []intake.Epic, id string) (intake.Group, bool) {
	for _, e := range epics {
		for _, g := range e.Groups {
			if g.ID == id {
				return g, true
			}
		}
	}
	return intake.Group{}, false
}

// progressOf converts a saved group record into the Task Manager's counters.
func progressOf(st engine.State) *task.Progress {
	p := &task.Progress{Stage: st.Stage, Widened: slices.Clone(st.ScopeAdded)}
	if u := st.Unfinished; u != nil {
		p.Unfinished = &state.UnfinishedEdits{Task: u.Task, Files: slices.Clone(u.Files)}
	}

	return p
}

// reportFact sends a fact input to the group's loop.
func reportFact(ctx context.Context, m *engine.Manager, group, subtype string, data any) error {
	if m == nil {
		return nil
	}
	raw, _ := jsonOf(data)
	err := m.Report(ctx, engine.Input{Kind: engine.InFact, Group: group, Time: time.Now(),
		Data: jsonRaw(engine.FactData{Subtype: subtype, Data: raw})})
	return err
}

// commandTable turns the commands detected in the group's tree into the Task Manager's command classes and names the tree's
// language.
func commandTable(cs contextmgr.CommandSet) (map[string][]string, string) {
	cmds := map[string][]string{}
	lang := ""
	for _, c := range cs.Commands {
		if c.Class == contextmgr.ClassGitRead {
			continue
		}
		cmds[string(c.Class)] = slices.Clone(c.Argv)
		if l := markerLanguage[c.Marker]; l != "" && lang == "" {
			lang = l
		}
	}

	return cmds, lang
}

// toolSetup is the commands the checks and the baseline read for the group's language.
type toolSetup struct {
	Name     string // gradle, maven or pytest; empty when the language profile has no entry
	Test     string // the test class as one shell line
	JUnit    string // prints the JUnit XML reports of a finished test run; empty when none is confirmed
	Coverage string // runs the tests and prints the coverage report; empty keeps the built-in default
}

// resolveTools names a Java or Kotlin tree's language from its build file, fills the test class the repository profile
// found none for, and returns the language profile's commands that fit the test command.
func resolveTools(p agent.ToolProfiles, t contextmgr.Tree, cmds map[string][]string, lang string) (string, toolSetup) {
	if lang == "" {
		switch {
		case t.Exists("build.gradle.kts"):
			lang = "kotlin"
		case t.Exists("build.gradle"), t.Exists("settings.gradle"), t.Exists("settings.gradle.kts"), t.Exists("pom.xml"):
			lang = "java"
		}
	}

	var ts toolSetup
	if r, ok := p.Resolve(lang, t.Exists, cmds["test"]); ok {
		ts = toolSetup{Name: r.Name, JUnit: r.JUnit, Coverage: r.Coverage}
		if r.Test != "" {
			cmds["test"] = []string{"sh", "-c", r.Test}
		}
	}

	ts.Test = shellLine(cmds["test"])
	return lang, ts
}

// shellLine renders a command as one shell line; a command that is already `sh -c <line>` gives its line.
func shellLine(argv []string) string {
	if len(argv) == 3 && argv[0] == "sh" && argv[1] == "-c" {
		return argv[2]
	}

	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = a
		if a == "" || strings.ContainsAny(a, " \t\n'\"\\$&|;<>()*?[]#~`!{}") {
			parts[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}

	return strings.Join(parts, " ")
}

// baselineStore keeps the baseline in the State Manager, beside the group's record, and finds the entry of any group.
type baselineStore struct {
	st    *state.Manager
	group string
}

// Load returns the baseline any group stored for the tree.
func (b baselineStore) Load(_ context.Context, tree string) (agent.Baseline, bool, error) {
	r, ok, err := b.st.FindBaseline(tree)
	if err != nil || !ok {
		return agent.Baseline{}, false, err
	}
	return agent.Baseline{TreeHash: r.Tree, Passed: r.Passed}, true, nil
}

// Save stores the baseline beside the group's record.
func (b baselineStore) Save(_ context.Context, bl agent.Baseline) error {
	return b.st.SaveBaseline(b.group, state.BaselineRecord{Tree: bl.TreeHash, Passed: bl.Passed})
}

// checkoutBase writes the base commit's files into a folder under the group's build folder, which the Tester's sandbox can
// read and write, and returns the folder. A finished checkout is reused.
func checkoutBase(ctx context.Context, root, base, buildDir string) (string, error) {
	// doneMark is the file written last, so a half-written checkout is made again.
	const doneMark = ".komodo-base"

	dir := filepath.Join(buildDir, "base-"+base[:min(12, len(base))])
	if _, err := os.Stat(filepath.Join(dir, doneMark)); err == nil {
		return dir, nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	archive := exec.CommandContext(ctx, "git", "-C", root, "archive", base)
	untar := exec.CommandContext(ctx, "tar", "-x", "-C", dir)

	pipe, err := archive.StdoutPipe()
	if err != nil {
		return "", err
	}

	untar.Stdin = pipe
	if err := untar.Start(); err != nil {
		return "", fmt.Errorf("start tar: %w", err)
	}
	if err := archive.Start(); err != nil {
		_ = untar.Process.Kill()
		_ = untar.Wait()
		return "", fmt.Errorf("start git archive: %w", err)
	}

	aerr := archive.Wait()
	if err := untar.Wait(); err != nil || aerr != nil {
		return "", fmt.Errorf("extract %s: %w", base, errors.Join(aerr, err))
	}

	return dir, os.WriteFile(filepath.Join(dir, doneMark), nil, 0o644)
}

// permissionsOf turns an agent's configuration into the permissions its guard and tool registry enforce.
func permissionsOf(a config.Agent) task.Permissions {
	rows := make([]task.RowID, len(a.Guard))
	for i, r := range a.Guard {
		rows[i] = task.RowID(r)
	}

	return task.Permissions{
		Tools:      a.Tools,
		RunClasses: a.RunClasses,
		Guard:      rows,
		Weight:     string(a.Weight),
		Chain:      a.Chain,
	}
}
