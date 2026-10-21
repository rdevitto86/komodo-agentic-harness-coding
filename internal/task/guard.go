package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// Call is one tool request as the guard sees it.
type Call struct {
	Tool string          // tool name as requested
	Args json.RawMessage // arguments exactly as received
}

// Verdict is the guard's decision for one call.
type Verdict struct {
	Allowed     bool
	Row         RowID               // the refusing row; empty when allowed
	Message     string              // the refusal line naming the row
	Alternative string              // what the model may do instead
	EndSession  bool                // true on the refusal that reaches guard.refusal_limit
	Count       int                 // refusals under Row so far in this session
	Target      string              // the refused path, class or tool, for the tool.denied fact
	res         map[string]resolved // the paths the guard resolved for an allowed call, keyed by the path as requested
}

// Binding is what the guard needs from an agent and its group.
type Binding struct {
	Agent       string
	Permissions Permissions
	Worktree    string   // absolute path of the session worktree
	Files       []string // the group's files globs; builder only
}

// Guard holds the rows compiled for one session.
type Guard struct {
	pol   *policy
	b     Binding
	limit int
	order []Row            // the rows that bind this agent, in policy order
	byID  map[RowID]Row    // every row, for refusal templates
	text  []*regexp.Regexp // the G5 patterns

	mu     sync.Mutex
	files  []string
	counts map[RowID]int
	ended  bool
}

// NewGuard compiles the rows that bind this agent into one matcher. It fails when a floor row is missing, a row has the
// wrong kind or an unknown id, or a pattern does not compile.
func NewGuard(rows []Row, b Binding, refusalLimit int) (*Guard, error) {
	pol, err := loadPolicy()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errNoPolicy, err)
	}
	if refusalLimit < 1 {
		return nil, errors.New("the refusal limit must be at least 1")
	}
	if !filepath.IsAbs(b.Worktree) {
		return nil, errors.New("the binding has no absolute worktree path")
	}

	g := &Guard{
		pol:    pol,
		b:      b,
		limit:  refusalLimit,
		byID:   map[RowID]Row{},
		counts: map[RowID]int{},
		files:  slices.Clone(b.Files),
	}

	for _, r := range rows {
		want, ok := kinds[r.ID]
		switch {
		case !ok:
			return nil, fmt.Errorf("unknown guard row %q", r.ID)
		case r.Kind != want || r.Action != "refuse":
			return nil, fmt.Errorf("guard row %s must be kind %s with action refuse", r.ID, want)
		}
		if _, dup := g.byID[r.ID]; dup {
			return nil, fmt.Errorf("guard row %s is listed twice", r.ID)
		}

		g.byID[r.ID] = r
	}

	for _, id := range mandatoryRows {
		if _, ok := g.byID[id]; !ok {
			return nil, fmt.Errorf("guard row %s is missing", id)
		}
	}

	for _, id := range b.Permissions.Guard {
		if _, ok := g.byID[id]; !ok {
			return nil, fmt.Errorf("guard row %s named in the permissions does not exist", id)
		}
	}
	if slices.Contains(b.Permissions.Tools, "run") {
		if _, ok := g.byID["R1"]; !ok {
			return nil, errors.New("guard row R1 is missing for an agent that can run commands")
		}
	}

	for _, r := range rows {
		if r.selects(b) {
			g.order = append(g.order, r)
		}
	}
	if slices.Contains(b.Permissions.Tools, "run") &&
		!slices.ContainsFunc(g.order, func(r Row) bool { return r.ID == "R1" }) {
		g.order = append(g.order, g.byID["R1"])
	}

	for _, p := range g.byID["G5"].Pattern {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("compile G5 pattern %q: %w", p, err)
		}
		g.text = append(g.text, re)
	}

	return g, nil
}

// SetFiles replaces the group's file list after a scope answer.
func (g *Guard) SetFiles(files []string) {
	g.mu.Lock()
	g.files = slices.Clone(files)
	g.mu.Unlock()
}

// Refusals returns the refusals counted per row in this session.
func (g *Guard) Refusals() map[RowID]int {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make(map[RowID]int, len(g.counts))
	for k, v := range g.counts {
		out[k] = v
	}
	return out
}

// Check decides one call; unparsable input, an unknown tool or an internal error is a refusal, never an allow.
func (g *Guard) Check(c Call) (v Verdict) {
	defer g.refuseOnPanic(c.Tool, &v)

	g.mu.Lock()
	ended, files := g.ended, slices.Clone(g.files)
	g.mu.Unlock()
	if ended {
		return g.finish(refuse("G6", c.Tool, nil))
	}

	res := map[string]resolved{}
	if r := g.evaluate(c, files, res); r != nil {
		return g.finish(r)
	}

	return Verdict{Allowed: true, res: res}
}

// ScopeDenied reports the row that forbids a scope request for p: G2 outside the worktree, G3 protected, G4 secret.
func (g *Guard) ScopeDenied(p string) (RowID, bool) {
	if strings.HasPrefix(p, "~") {
		return "G2", true
	}

	r, why := resolveInside(g.b.Worktree, p)
	if why != "" {
		return "G2", true
	}
	if row, ok := g.byID["G3"]; ok && matchAny(row.Pattern, r.Rel, r.Abs, true, true) {
		return "G3", true
	}
	if g.IsSecretFile(r.Rel) {
		return "G4", true
	}

	return "", false
}

// IsSecretFile reports whether a worktree-relative path names a secret file.
func (g *Guard) IsSecretFile(rel string) bool {
	row, ok := g.byID["G4"]
	return ok && matchAny(row.Pattern, rel, "", true, true)
}

// SecretFiles lists the absolute paths of the secret files in the worktree, for the sandbox to mask.
func (g *Guard) SecretFiles() []string { return secretFiles(g.b.Worktree, g.byID["G4"].Pattern) }

// refuseOnPanic turns a panic inside a check into a refusal under G1; it runs deferred and sets the named verdict.
func (g *Guard) refuseOnPanic(tool string, v *Verdict) {
	if r := recover(); r != nil {
		*v = g.finish(
			refuse(
				"G1",
				tool,
				map[string]string{
					"why":   "the guard failed on this call",
					"tools": g.toolList(),
				},
			),
		)
	}
}

// finish counts a refusal against its row and builds the verdict.
func (g *Guard) finish(r *refusal) Verdict {
	g.mu.Lock()
	g.counts[r.row]++

	n := g.counts[r.row]
	if n >= g.limit {
		g.ended = true
	}

	end := g.ended
	g.mu.Unlock()
	row := g.byID[r.row]
	r.vars["row"], r.vars["count"] = string(r.row), strconv.Itoa(n)

	v := Verdict{Row: r.row, Target: r.target, Count: n, EndSession: end,
		Message:     g.pol.render(row.Message, r.vars),
		Alternative: g.pol.render(row.Alternative, r.vars)}
	if end && r.row != "G6" {
		v.Message += " " + g.pol.render(g.byID["G6"].Message, r.vars)
	}

	return v
}

// evaluate runs the rows in policy order against one call and returns the first refusal.
func (g *Guard) evaluate(c Call, files []string, res map[string]resolved) *refusal {
	perm := g.b.Permissions
	switch {
	case len(perm.Tools) == 0:
		return refuse("G1", c.Tool, map[string]string{"why": "no tool is permitted for this agent", "tools": "none"})
	case !slices.Contains(perm.Tools, c.Tool):
		return refuse(
			"G1",
			c.Tool,
			map[string]string{
				"why":   "the tool " + strconv.Quote(c.Tool) + " is not in your permissions",
				"tools": g.toolList(),
			},
		)
	}

	pc, bad := parseCall(c)
	if bad != nil {
		bad.vars["tools"] = g.toolList()
		bad.vars["classes"] = strings.Join(perm.RunClasses, ", ")
		return bad
	}

	if pc.outside {
		server, _, _ := strings.Cut(c.Tool, ".")
		if !slices.Contains(perm.Servers, server) {
			return refuse(
				"G1",
				c.Tool,
				map[string]string{
					"why": "the outside tool server " + strconv.Quote(
						server,
					) + " is not switched on for you",
					"tools": g.toolList(),
				},
			)
		}

		return nil
	}

	var reads, writes []resolved
	for _, p := range pc.reads {
		r, why := resolveInside(g.b.Worktree, p)
		if why != "" {
			return refuse("G2", p, map[string]string{"why": why})
		}
		reads = append(reads, r)
		res[p] = r
	}

	for _, p := range pc.writes {
		r, why := resolveInside(g.b.Worktree, p)
		if why != "" {
			return refuse("G2", p, map[string]string{"why": why})
		}
		writes = append(writes, r)
		res[p] = r
	}

	for _, row := range g.order {
		var r *refusal

		switch row.ID {
		case "G3":
			r = g.checkProtected(row, pc.writes, writes)
		case "G4":
			r = g.checkSecrets(row, pc, reads, writes)
		case "G5":
			r = g.checkText(pc.texts)
		case "R1":
			r = g.checkRun(pc.run)
		case "B1":
			r = g.checkScope(writes, files)
		}

		if r != nil {
			return r
		}
	}

	return nil
}

// toolList names the agent's permitted tools for a refusal line.
func (g *Guard) toolList() string { return strings.Join(g.b.Permissions.Tools, ", ") }

// checkProtected refuses a write to a protected path (row G3).
func (g *Guard) checkProtected(row Row, typed []string, writes []resolved) *refusal {
	for i, w := range writes {
		for _, s := range spellings(typed[i], w) {
			if matchAny(row.Pattern, s, w.Abs, true, true) {
				return refuse(row.ID, w.Rel, nil)
			}
		}
	}
	return nil
}

// checkSecrets refuses reads of secret files and writes of secret content (row G4).
func (g *Guard) checkSecrets(row Row, pc parsed, reads, writes []resolved) *refusal {
	all := append(slices.Clone(reads), writes...)

	typed := append(slices.Clone(pc.reads), pc.writes...)
	for i, r := range all {
		for _, s := range spellings(typed[i], r) {
			if matchAny(row.Pattern, s, r.Abs, true, true) {
				return refuse(row.ID, r.Rel, map[string]string{"why": r.Rel + " is a secret file"})
			}
		}
	}

	for _, t := range pc.rev {
		if matchAny(row.Pattern, t, "", true, true) {
			return refuse(row.ID, t, map[string]string{"why": t + " is a secret file"})
		}
	}

	for _, t := range pc.texts {
		for _, re := range g.pol.secret {
			if re.MatchString(t) {
				return refuse(row.ID, "", map[string]string{"why": "the text you write matches a secret pattern"})
			}
		}
	}

	return nil
}

// checkText refuses text that carries an attribution trailer or a session link (row G5).
func (g *Guard) checkText(texts []string) *refusal {
	for _, t := range texts {
		for _, re := range g.text {
			if re.MatchString(t) {
				return refuse("G5", "", nil)
			}
		}
	}
	return nil
}

// checkRun refuses a command class the agent's permissions do not list (row R1).
func (g *Guard) checkRun(run *RunArgs) *refusal {
	if run == nil {
		return nil
	}

	classes := strings.Join(g.b.Permissions.RunClasses, ", ")
	if !slices.Contains(g.b.Permissions.RunClasses, run.Class) {
		return refuse(
			"R1",
			run.Class,
			map[string]string{
				"why":     "the class " + strconv.Quote(run.Class) + " is not one you may run",
				"classes": classes,
			},
		)
	}

	return nil
}

// checkScope refuses a write outside the group's files and their test siblings (row B1).
func (g *Guard) checkScope(writes []resolved, files []string) *refusal {
	for _, w := range writes {
		if !inFiles(w.Rel, files) && !g.pol.siblingOf(w.Rel, files, g.b.Worktree) {
			return refuse("B1", w.Rel, map[string]string{"files": strings.Join(files, ", ")})
		}
	}
	return nil
}

// refusal is a refused call before it is counted.
type refusal struct {
	row    RowID
	target string
	vars   map[string]string
}

// parsed is a call after its arguments were read.
type parsed struct {
	reads, writes []string // path arguments
	texts         []string // text the call writes
	rev           []string // paths named inside git revision arguments
	run           *RunArgs
	outside       bool // a tool of an outside server
}

// toolArgs are the argument objects of the built-in tools.
type (
	readArgs struct {
		Path  string `json:"path"`
		Start int    `json:"start,omitempty"`
		End   int    `json:"end,omitempty"`
	}
	searchArgs struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path,omitempty"`
	}
	editArgs struct {
		Path string `json:"path"`
		Old  string `json:"old"`
		New  string `json:"new"`
	}
	writeArgs struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	diffArgs struct {
		Paths []string `json:"paths,omitempty"`
	}
	gitReadArgs struct {
		Command string   `json:"command"`
		Args    []string `json:"args,omitempty"`
	}
	skillArgs struct {
		Name string `json:"name"`
	}
	findingArgs struct {
		Rule  string `json:"rule"`
		File  string `json:"file"`
		Line  int    `json:"line"`
		Quote string `json:"quote"`
		Note  string `json:"note"`

		Proof *agent.Proof `json:"proof,omitempty"`
	}
	doneArgs struct {
		Task string `json:"task"`
	}
	lintArgs struct {
		Draft string `json:"draft"`
	}
	checkArgs struct {
		Item string `json:"item"`
	}
)

// RunArgs is the argument object of the run tool.
type RunArgs struct {
	Class string   `json:"class"` // a command class listed in the agent's run_classes
	Args  []string `json:"args"`  // passed to the class's command as separate arguments
}

// kinds lists the kind each shipped row must have.
var kinds = map[RowID]string{
	"G1": "tool",
	"G2": "path",
	"G3": "path",
	"G4": "path",
	"G5": "text",
	"G6": "tool",
	"R1": "run",
	"B1": "path",
}

// skillName is the shape of a skill reference: a name with an optional section.
var skillName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*(#[A-Za-z0-9][A-Za-z0-9 _.-]*)?$`)

// gitFlag is the set of flags git_read passes on; anything else could write a file or change a setting.
var gitFlag = regexp.MustCompile(
	`^(-n\d*|-\d+|-p|-s|-w|-M|-L\d*(,\d+)?|--(oneline|stat|shortstat|numstat|summary|` +
		`name-only|name-status|short|porcelain|branch|no-color|graph|decorate|abbrev-commit|` +
		`patch|no-patch|follow|first-parent|merges|no-merges|reverse|date=[a-z-]+|abbrev=\d+|` +
		`max-count=\d+|since=[^\s;|&]+|until=[^\s;|&]+|author=[^\s]+|diff-filter=[A-Za-z]+))$`,
)

// gitCommands are the read-only git commands git_read runs.
var gitCommands = []string{"log", "show", "blame", "status"}

// spellings returns the paths a request names: the resolved relative path and, when different, the one as typed.
func spellings(typed string, r resolved) []string {
	out := []string{r.Rel}
	if s := path.Clean(filepath.ToSlash(typed)); s != r.Rel && !path.IsAbs(s) {
		out = append(out, s)
	}
	return out
}

// parseCall reads a call's arguments. A call that cannot be read comes back as a refusal under the row that owns its
// arguments.
func parseCall(c Call) (parsed, *refusal) {
	var p parsed

	unreadable := func(row RowID, err error) (parsed, *refusal) {
		return parsed{}, refuse(row, c.Tool, map[string]string{"why": "its arguments cannot be read (" + err.Error() + ")"})
	}

	switch c.Tool {
	case "read":
		var a readArgs
		if err := decode(c.Args, &a); err != nil {
			return unreadable("G2", err)
		}
		p.reads = []string{a.Path}
	case "search":
		var a searchArgs
		if err := decode(c.Args, &a); err != nil {
			return unreadable("G2", err)
		}
		p.reads = []string{cmpOr(a.Path, ".")}
	case "edit":
		var a editArgs
		if err := decode(c.Args, &a); err != nil {
			return unreadable("G2", err)
		}
		p.reads, p.writes, p.texts = []string{a.Path}, []string{a.Path}, []string{a.New}
	case "write":
		var a writeArgs
		if err := decode(c.Args, &a); err != nil {
			return unreadable("G2", err)
		}
		p.writes, p.texts = []string{a.Path}, []string{a.Content}
	case "run":
		var a RunArgs
		if err := decode(c.Args, &a); err != nil {
			return unreadable("R1", err)
		}
		p.run = &a
	case "diff":
		var a diffArgs
		if err := decode(c.Args, &a); err != nil {
			return unreadable("G2", err)
		}

		for _, s := range a.Paths {
			if strings.HasPrefix(s, ":") {
				return parsed{}, refuse(
					"G2",
					s,
					map[string]string{
						"why": "is a pathspec with magic, which diff does not accept",
					},
				)
			}
		}

		p.reads = a.Paths
	case "git_read":
		var a gitReadArgs
		if err := decode(c.Args, &a); err != nil {
			return unreadable("G2", err)
		}
		return parseGitRead(c, a)
	case "skill":
		var a skillArgs
		if err := decode(c.Args, &a); err != nil || !skillName.MatchString(a.Name) {
			return parsed{}, refuse("G1", c.Tool, map[string]string{"why": "the skill name is not a skill name"})
		}
	case "submit_finding":
		var a findingArgs
		if err := decode(c.Args, &a); err != nil {
			return unreadable("G2", err)
		}
		p.reads = []string{a.File}
	case "task_done":
		var a doneArgs
		if err := decode(c.Args, &a); err != nil {
			return unreadable("G1", err)
		}
	case "run_check":
		var a checkArgs
		if err := decode(c.Args, &a); err != nil {
			return unreadable("G1", err)
		}
	case "lint_backlog":
		var a lintArgs
		if err := decode(c.Args, &a); err != nil {
			return unreadable("G1", err)
		}
	default:
		if !strings.Contains(c.Tool, ".") {
			return parsed{}, refuse(
				"G1",
				c.Tool,
				map[string]string{"why": "the tool " + strconv.Quote(c.Tool) + " does not exist"},
			)
		}

		var obj map[string]json.RawMessage
		if err := decode(c.Args, &obj); err != nil {
			return unreadable("G1", err)
		}

		p.outside = true
	}

	return p, nil
}

// refuse builds a refusal under a row with the words of its template.
func refuse(row RowID, target string, vars map[string]string) *refusal {
	if vars == nil {
		vars = map[string]string{}
	}
	vars["path"] = target
	return &refusal{row: row, target: target, vars: vars}
}

// decode reads an argument object strictly; no arguments mean an empty object.
func decode(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	return core.DecodeStrict(raw, v)
}

// cmpOr returns s, or def when s is empty.
func cmpOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// parseGitRead checks a git_read request: a read-only command, allowed flags, and arguments that name paths inside the
// worktree.
func parseGitRead(c Call, a gitReadArgs) (parsed, *refusal) {
	var p parsed

	bad := func(why string) (parsed, *refusal) {
		return parsed{}, refuse("G2", c.Tool, map[string]string{"why": why})
	}
	if !slices.Contains(gitCommands, a.Command) {
		return bad("git_read runs only " + strings.Join(gitCommands, ", "))
	}

	paths := false

	for _, s := range a.Args {
		switch {
		case s == "--" && !paths:
			paths = true
		case strings.ContainsRune(s, 0):
			return bad("an argument holds a NUL byte")
		case !paths && strings.HasPrefix(s, "-"):
			if !gitFlag.MatchString(s) {
				return bad("the flag " + strconv.Quote(s) + " is not one git_read passes on")
			}
		case paths && strings.HasPrefix(s, ":"):
			return bad("the pathspec " + strconv.Quote(s) + " uses magic the guard cannot classify")
		case paths:
			p.reads = append(p.reads, s)
		default:
			file, has, ok := objectPath(s)
			switch {
			case !ok:
				return bad("the argument " + strconv.Quote(s) + " names a path the guard cannot classify")
			case has:
				p.rev = append(p.rev, file)
			default:
				p.reads = append(p.reads, s)
			}
		}
	}

	return p, nil
}

// objectPath reads a revision argument the way git does: "<rev>:<path>" names a file in a tree, ":<path>" and
// ":<stage>:<path>" a file in the index, and a colon inside {...} belongs to the
// revision. ok is false when the argument holds a path the guard cannot classify.
func objectPath(s string) (file string, has, ok bool) {
	if !strings.Contains(s, ":") {
		return "", false, true
	}

	depth, cut := 0, -1
	for i := 0; i < len(s) && cut < 0; i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
		case ':':
			if depth == 0 && i > 0 {
				cut = i
			}
		}
	}

	if depth != 0 {
		return "", false, false
	}

	switch {
	case strings.HasPrefix(s, ":/"):
		return "", false, true
	case strings.HasPrefix(s, ":"):
		file = s[1:]
		if len(file) >= 2 && file[0] >= '0' && file[0] <= '3' && file[1] == ':' {
			file = file[2:]
		}
	case cut >= 0:
		file = s[cut+1:]
	default:
		return "", false, true
	}

	if file == "" {
		return "", false, true
	}
	if strings.Contains(file, ":") {
		return "", false, false
	}

	file = path.Clean(file)
	if file == ".." || strings.HasPrefix(file, "../") || path.IsAbs(file) {
		return "", false, false
	}

	return file, true, true
}
