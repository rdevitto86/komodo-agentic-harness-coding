package contextmgr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	komodo "github.com/rdevitto86/komodo-agentic-harness-coding"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cache"
)

// fakeTree is an in-memory repository at one commit.
type fakeTree struct {
	files  map[string]string
	hash   string
	onRead func(path string)
}

// newTree returns a tree over the given path-to-content map.
func newTree(files map[string]string) *fakeTree { return &fakeTree{files: files} }

// Hash returns the fixed hash, or one computed from every path and content.
func (f *fakeTree) Hash() string {
	if f.hash != "" {
		return f.hash
	}
	h := sha256.New()
	for _, p := range f.Paths() {
		_, _ = io.WriteString(h, p+"\x00"+f.files[p]+"\x00")
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Paths returns the file paths in byte order.
func (f *fakeTree) Paths() []string {
	out := make([]string, 0, len(f.files))
	for p := range f.files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// ReadFile returns a file's content or fs.ErrNotExist.
func (f *fakeTree) ReadFile(p string) ([]byte, error) {
	if f.onRead != nil {
		f.onRead(p)
	}
	s, ok := f.files[p]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return []byte(s), nil
}

// Exists reports whether some path matches the glob.
func (f *fakeTree) Exists(pattern string) bool {
	for p := range f.files {
		if matchGlob(pattern, p) {
			return true
		}
	}
	return false
}

// fakeCache is an in-memory cache.Handle that can fail or damage what it returns.
type fakeCache struct {
	mu     sync.Mutex
	m      map[string][]byte
	gets   int
	puts   int
	fail   bool
	mangle func([]byte) []byte
}

// newFakeCache returns an empty cache.
func newFakeCache() *fakeCache { return &fakeCache{m: map[string][]byte{}} }

// Key hashes the inputs into a 64-character key.
func (c *fakeCache) Key(inputs ...string) cache.Key {
	sum := sha256.Sum256([]byte(strings.Join(inputs, "\x00")))
	return cache.Key(hex.EncodeToString(sum[:]))
}

// Get returns a stored payload, damaged by mangle when set; a failing cache always misses.
func (c *fakeCache) Get(ns cache.Namespace, key cache.Key) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gets++
	if c.fail {
		return nil, false
	}
	b, ok := c.m[string(ns)+"/"+string(key)]
	if ok && c.mangle != nil {
		b = c.mangle(append([]byte(nil), b...))
	}
	return b, ok
}

// Put stores a payload; a failing cache returns an error.
func (c *fakeCache) Put(ns cache.Namespace, key cache.Key, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.puts++
	if c.fail {
		return errors.New("cache down")
	}
	c.m[string(ns)+"/"+string(key)] = append([]byte(nil), data...)
	return nil
}

// workers and steerList are the allow list and the steering phrases the tests start from.
var (
	workers   = []string{"builder", "reviewer", "tester"}
	steerList = []string{"ignore previous instructions"}
)

// defaultItems returns a starting table that enables every shipped skill for the workers.
func defaultItems() []ItemState {
	var out []ItemState
	for _, n := range []SkillName{
		"standards-comments",
		"standards-go",
		"standards-kotlin",
		"standards-ui-mobile",
		"standards-aws",
	} {
		st := ItemState{Name: n, Origin: OriginEmbedded, Enabled: true, Allow: append([]string(nil), workers...)}
		if n == "standards-comments" {
			st.Always = []string{"builder"}
		}
		out = append(out, st)
	}
	return out
}

// testDeps returns dependencies over a fresh cache and an empty home folder.
func testDeps(t *testing.T) Deps {
	t.Helper()
	return Deps{
		Cache:   newFakeCache(),
		Items:   defaultItems(),
		Phrases: func() []string { return steerList },
		Home:    t.TempDir(),
		Tools: map[string][]string{
			"planner": {"read", "search"}, "builder": {"read", "edit", "run", "skill"},
			"tester": {"read", "run"}, "reviewer": {"read", "search"},
		},
	}
}

// newManager returns a manager over testDeps after the tweaks have run.
func newManager(t *testing.T, tweaks ...func(*Deps)) *Manager {
	t.Helper()
	d := testDeps(t)
	for _, f := range tweaks {
		f(&d)
	}
	m, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m
}

// slotsFor returns caller values for every required slot the caller owns.
func slotsFor(agent string) Values {
	v := Values{}
	switch agent {
	case "planner":
		v[Goal] = "add a lock"
	case "builder":
		v[Group], v[Tasks], v[Files], v[FileContents] = "tg-1", "task list", "a.go", "package a"
	case "tester":
		v[Group], v[Tasks], v[Acceptance] = "tg-1", "task list", "item 1"
	case "reviewer":
		v[Group], v[Tasks], v[Files], v[Diff] = "tg-1", "task list", "a.go", "diff text"
		v[Lens], v[LensRules], v[Checks] = "correctness", "rules", "clean"
	}
	return v
}

// goTree is a small Go repository.
func goTree() *fakeTree {
	return newTree(map[string]string{
		"go.mod":          "module x\n\ngo 1.27\n",
		"AGENTS.md":       "# Repo\n\nUse tabs.\n",
		"internal/a/a.go": "package a\n\n// Alpha does a thing.\nfunc Alpha() {}\n",
		"internal/b/b.go": "package b\n\nfunc Beta() { a.Alpha() }\n",
	})
}

// embeddedCopy copies the binary's embedded files into a map file system the test can change.
func embeddedCopy(t *testing.T) fstest.MapFS {
	t.Helper()
	out := fstest.MapFS{}
	err := fs.WalkDir(komodo.Embedded, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(komodo.Embedded, p)
		if err != nil {
			return err
		}
		out[p] = &fstest.MapFile{Data: b}
		return nil
	})
	if err != nil {
		t.Fatalf("copy embedded: %v", err)
	}
	return out
}

// writeLocalSkill writes a SKILL.md under home/skills/<name>/ and returns its path.
func writeLocalSkill(t *testing.T, home, name, text string) string {
	t.Helper()
	dir := filepath.Join(home, "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// localSkillText returns a valid SKILL.md with one section and the given extra body line.
func localSkillText(name, extra string) string {
	return "---\nv: 1\nname: " + name +
		"\ndescription: \"A local skill\"\nglobs:\n  - \"*.go\"\nmarkers: []\nsections:\n  - \"Rules\"\n---\n## Rules\n- " +
		"**Always** keep it short.\n" +
		extra + "\n"
}

// get runs GetContext with the tree and files and fails the test on error.
func get(t *testing.T, m *Manager, agent string, tree Tree, files ...string) Context {
	t.Helper()
	c, err := m.GetContext(context.Background(), Request{
		Agent: agent,
		Run:   "r",
		Group: "g",
		Tree:  tree,
		Files: files,
		Slots: slotsFor(agent),
	})
	if err != nil {
		t.Fatalf("GetContext(%s): %v", agent, err)
	}
	return c
}

// names lists the skill names of a selection.
func names(sel []Selected) []string {
	var out []string
	for _, s := range sel {
		out = append(out, string(s.Name))
	}
	return out
}
