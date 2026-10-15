package contextmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"sync"

	komodo "github.com/rdevitto86/komodo-agentic-harness-coding"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cache"
)

// Request is what a caller hands GetContext.
type Request struct {
	Agent string // planner, builder, tester or reviewer
	Run   string
	Group string      // empty for the planner
	Tree  Tree        // read-only view of the base commit
	Files []string    // the group's files; selects skills; empty for the planner
	Slots Values      // slot values only the caller has: goal, group, tasks, diff, findings and so on
	Chain []Candidate // the agent's link chain with measured contexts; sizes the repo map
}

// Tree is a read-only view of the repository at one commit.
type Tree interface {
	Hash() string                         // content fingerprint of the tree
	Paths() []string                      // tracked file paths in byte order
	ReadFile(path string) ([]byte, error) // the file's bytes at this commit
	Exists(pattern string) bool           // marker lookup: some path matches the glob
}

// Context is what GetContext returns: a brief and what goes with it.
type Context struct {
	Brief       Brief
	Skills      []Selected   // the standards in the brief, in order
	Tools       []Connection // enabled connections this agent may use, the embedded one first
	Commands    CommandSet   // detected setup, build, test, lint and format commands
	Warnings    []Warning    // problems that did not stop the brief
	BriefTokens int          // size of all 5 parts
}

// Warning is a problem GetContext reports without failing, such as a dropped paragraph.
type Warning struct {
	Code string // for example addendum_paragraph_dropped
	Path string
	Note string
}

// Publisher is the part of the Event Bus the Context Manager uses.
type Publisher interface {
	Publish(e bus.Event) error
	Subscribe(name string, types ...bus.Type) (*bus.Subscription, error)
}

// Deps are the collaborators the harness passes in at construction.
type Deps struct {
	Bus     Publisher
	Cache   cache.Handle    // the cache handle the Runtime Manager opens and passes in
	Items   []ItemState     // starting skill state, from the Configuration Manager
	Conns   []Connection    // starting connection state
	Phrases func() []string // steering phrases, from the Task Manager's guard data
	Home    string          // Komodo home folder; the context folder is under it
	Count   Counter         // token counter; New uses the bundled one when nil

	Tools    map[string][]string // each agent's tool names, from the Agent Registry; part 1 of the brief lists them
	Embedded fs.FS               // the embedded files; New uses the binary's own when nil
}

// Manager assembles briefs and serves skills; its item table changes only through events.
type Manager struct {
	deps  Deps
	fsys  fs.FS
	count Counter
	langs []langPattern
	tpls  map[string]Template

	mu         sync.RWMutex // the item table's lock: a brief sees one consistent table
	items      []*item
	conns      []Connection // outside connections, in file order
	skillWarns []Warning    // problems found while loading local skills
	connWarns  []Warning    // problems found while loading connections.json
}

// New returns a Manager with its starting item table; it does not subscribe until Run is called.
func New(d Deps) (*Manager, error) {
	m := &Manager{deps: d, fsys: d.Embedded, count: counterOr(d.Count)}
	if m.fsys == nil {
		m.fsys = komodo.Embedded
	}
	idx, err := loadIndex(m.fsys)
	if err != nil {
		return nil, err
	}
	if m.langs, err = loadLangs(m.fsys); err != nil {
		return nil, err
	}
	if m.tpls, err = loadTemplates(m.fsys); err != nil {
		return nil, err
	}
	if err := m.startItems(idx); err != nil {
		return nil, err
	}
	m.startConnections()
	return m, nil
}

// Run subscribes to skill and mcp events and applies them in arrival order until ctx ends.
func (m *Manager) Run(ctx context.Context) error {
	if m.deps.Bus == nil {
		return errors.New("no event bus to subscribe to")
	}
	sub, err := m.deps.Bus.Subscribe("contextmgr", bus.TypeSkill, bus.TypeMCP)
	if err != nil {
		return fmt.Errorf("subscribe to skill and mcp events: %w", err)
	}
	defer sub.Close()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-sub.Events:
			if !ok {
				return nil
			}
			m.apply(ev)
		}
	}
}

// Warnings returns the problems found while loading local skills and connections.
func (m *Manager) Warnings() []Warning {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.loadWarnings()
}

// loadWarnings returns the load warnings; the caller holds the table lock.
func (m *Manager) loadWarnings() []Warning {
	return append(append([]Warning(nil), m.skillWarns...), m.connWarns...)
}

// commands returns the detected commands, cached under the hash of the marker files.
func (m *Manager) commands(t Tree) CommandSet {
	hash := markerHash(t)
	c := m.deps.Cache
	if c == nil {
		return detectWith(t, hash)
	}
	key := c.Key("profile", hash)
	if b, ok := c.Get(cache.NSSetup, key); ok {
		var cs CommandSet
		if json.Unmarshal(b, &cs) == nil && cs.MarkerHash == hash {
			return cs
		}
	}
	cs := detectWith(t, hash)
	if b, err := json.Marshal(cs); err == nil {
		_ = c.Put(cache.NSSetup, key, b)
	}
	return cs
}

// repoMap returns the ranked map fitted to the token budget, cached under the tree, goal and budget.
func (m *Manager) repoMap(t Tree, goal string, budget int) RepoMap {
	c := m.deps.Cache
	var key cache.Key
	if c != nil {
		key = c.Key("map", t.Hash(), fingerprint([]byte(goal)), strconv.Itoa(budget))
		if b, ok := c.Get(cache.NSMap, key); ok {
			var rm RepoMap
			if json.Unmarshal(b, &rm) == nil && rm.TreeHash == t.Hash() {
				return rm
			}
		}
	}
	rm := buildRepoMap(t, goal, m.langs).Fit(budget)
	if c != nil {
		if b, err := json.Marshal(rm); err == nil {
			_ = c.Put(cache.NSMap, key, b)
		}
	}
	return rm
}

// mapBudget returns a tenth of the smallest ceiling in the chain, in tokens; 0 means no chain and no fitting.
func mapBudget(chain []Candidate) int {
	smallest := 0
	for _, c := range chain {
		if ce := Ceiling(c.Ctx); ce > 0 && (smallest == 0 || ce < smallest) {
			smallest = ce
		}
	}
	return smallest / 10
}

// renderTools renders part 1: the agent's tool names, then the tool servers it may use.
func renderTools(names []string, conns []Connection) string {
	var b strings.Builder
	b.WriteString("# Tools\n")
	for _, n := range names {
		b.WriteString("\n- " + n)
	}
	b.WriteString("\n\n# Tool servers\n")
	for _, c := range conns {
		b.WriteString("\n- " + c.Name)
	}
	return b.String()
}

// renderStandards renders part 4: each selected skill under a heading with its short fingerprint.
func renderStandards(sel []Selected) string {
	parts := make([]string, len(sel))
	for i, s := range sel {
		parts[i] = "## Standards: " + string(s.Name) + " (" + s.SHA8 + ")\n\n" + strings.TrimRight(s.Body, "\n")
	}
	return strings.Join(parts, "\n\n")
}

// GetContext assembles the brief and the tool list for one agent from the base-commit tree.
// It holds the table lock only to snapshot skills and tools, so an event never waits for a slow read.
func (m *Manager) GetContext(ctx context.Context, req Request) (Context, error) {
	if !validAgent(req.Agent) {
		return Context{}, fmt.Errorf("agent %q: %w", req.Agent, ErrAgentUnknown)
	}
	if req.Tree == nil {
		return Context{}, errors.New("request has no tree")
	}
	if err := ctx.Err(); err != nil {
		return Context{}, err
	}
	m.mu.RLock()
	skills, err := m.selectLocked(req.Agent, req.Files, req.Tree)
	tools, loadWarns := m.toolsFor(req.Agent), m.loadWarnings()
	m.mu.RUnlock()
	if err != nil {
		return Context{}, err
	}
	cs := m.commands(req.Tree)
	md, _ := req.Tree.ReadFile(agentsFile)
	addendum, addWarns := extractAddendum(string(md), m.phrases())
	standards := renderStandards(skills)
	vals := Values{}
	for s, v := range req.Slots {
		vals[s] = v
	}
	vals[RepoProfile] = renderProfile(detectLanguages(req.Tree, m.langs), cs)
	budget := mapBudget(req.Chain)
	if req.Agent == "planner" || (req.Agent == "reviewer" && budget > 0) {
		vals[SlotRepoMap] = m.repoMap(req.Tree, req.Slots[Goal], budget).Render()
	}
	if req.Agent == "planner" {
		vals[Layout] = renderLayout(req.Tree, budget)
	}
	guidance := ""
	if addendum != "" {
		guidance = "# Repository guidance\n\n" + addendum
	}
	br, err := buildBrief(req.Agent, m.tpls[req.Agent], renderTools(m.deps.Tools[req.Agent], tools), guidance, standards, vals)
	if err != nil {
		return Context{}, err
	}
	return Context{
		Brief: br, Skills: skills, Tools: slices.Clone(tools), Commands: cs,
		Warnings: append(loadWarns, addWarns...), BriefTokens: br.Tokens(m.count),
	}, nil
}
