package contextmgr

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	komodo "github.com/rdevitto86/komodo-agentic-harness-coding"
)

var update = flag.Bool("update", false, "rewrite embed/skills/index.json and rules.json from the skill files")

var registerOnce sync.Once

// RegisterOnce binds the event structs once per test process.
func RegisterOnce() { registerOnce.Do(RegisterEvents) }

// shippedSkillNames returns the folder names under embed/skills in order.
func shippedSkillNames(t *testing.T) []string {
	t.Helper()
	ents, err := fs.ReadDir(komodo.Embedded, "embed/skills")
	if err != nil {
		t.Fatalf("read embed/skills: %v", err)
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// buildIndex computes the index and rules files from the skill files on disk.
func buildIndex(t *testing.T, root string) (indexJSON, rulesJSON []byte) {
	t.Helper()
	dirs, _ := os.ReadDir(filepath.Join(root, "skills"))
	idx := Index{V: 1}
	rules := []Rule{}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, "skills", d.Name(), "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		fm, err := parseSkill(string(raw))
		if err != nil {
			t.Fatalf("%s: %v", d.Name(), err)
		}
		sum := sha256.Sum256(raw)
		idx.Skills = append(idx.Skills, IndexEntry{
			Name:        fm.Name,
			Description: fm.Description,
			Globs:       fm.Globs,
			Markers:     fm.Markers,
			Sections:    fm.Sections,
			SHA256:      hex.EncodeToString(sum[:]),
		})
		rules = append(rules, CompileRules(fm.Name, string(raw))...)
	}
	sort.Slice(idx.Skills, func(i, j int) bool { return idx.Skills[i].Name < idx.Skills[j].Name })
	var a, b bytes.Buffer
	for _, v := range []struct {
		buf *bytes.Buffer
		v   any
	}{{&a, idx}, {&b, rules}} {
		enc := json.NewEncoder(v.buf)
		enc.SetIndent("", "  ")
		if err := enc.Encode(v.v); err != nil {
			t.Fatal(err)
		}
	}
	return a.Bytes(), b.Bytes()
}

func TestShippedIndexAndRules(t *testing.T) {
	root := filepath.Join("..", "..", "embed")
	index, rules := buildIndex(t, filepath.Join(root))
	if *update {
		for name, b := range map[string][]byte{"index.json": index, "rules.json": rules} {
			if err := os.WriteFile(filepath.Join(root, "skills", name), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	for name, want := range map[string][]byte{"index.json": index, "rules.json": rules} {
		got, err := os.ReadFile(filepath.Join(root, "skills", name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is stale: run go test ./internal/contextmgr -run TestShippedIndexAndRules -update", name)
		}
	}
	if len(index) < 100 {
		t.Fatal("no shipped skill")
	}
}

func TestShippedSkills(t *testing.T) {
	var idx Index
	raw, err := fs.ReadFile(komodo.Embedded, "embed/skills/index.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &idx); err != nil {
		t.Fatal(err)
	}
	want := shippedSkillNames(t)
	var got []string
	for _, e := range idx.Skills {
		got = append(got, string(e.Name))
		body, err := fs.ReadFile(komodo.Embedded, "embed/skills/"+string(e.Name)+"/SKILL.md")
		if err != nil {
			t.Fatal(err)
		}
		if err := checkSkillSize(CountTokens, string(body)); err != nil {
			t.Errorf("%s: %v", e.Name, err)
		}
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != e.SHA256 {
			t.Errorf("%s: fingerprint differs from the index", e.Name)
		}
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("index lists %v, folders are %v", got, want)
	}
	rules, _ := fs.ReadFile(komodo.Embedded, "embed/skills/rules.json")
	var rows []Rule
	if err := json.Unmarshal(rules, &rows); err != nil {
		t.Fatal(err)
	}
	var goThird string
	for _, r := range rows {
		if r.ID == "std.go.3" {
			goThird = r.Line
		}
	}
	if goThird == "" || (!strings.HasPrefix(goThird, "Never") && !strings.HasPrefix(goThird, "Always")) {
		t.Fatalf("std.go.3 = %q", goThird)
	}
}

func TestShippedTemplates(t *testing.T) {
	for _, a := range Agents {
		raw, err := fs.ReadFile(komodo.Embedded, "embed/templates/"+a+".md")
		if err != nil {
			t.Fatalf("%s: %v", a, err)
		}
		tpl, err := ParseTemplate(a, string(raw))
		if err != nil {
			t.Fatalf("%s: %v", a, err)
		}
		if strings.Contains(strings.ToLower(tpl.Role), "{{") || tpl.Role == "" {
			t.Errorf("%s: role text", a)
		}
	}
}

func TestSlotTableMatchesRegistry(t *testing.T) {
	type reg struct {
		Slots struct {
			Required []string `json:"required"`
			Optional []string `json:"optional"`
		} `json:"slots"`
	}
	for _, a := range Agents {
		raw, err := fs.ReadFile(komodo.Embedded, "embed/agents/"+a+"/agent.json")
		if err != nil {
			t.Fatal(err)
		}
		var r reg
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		row := slotTable[a]
		var req []string
		for _, s := range row.required {
			req = append(req, string(s))
		}
		var opt []string
		for _, s := range row.optional {
			opt = append(opt, string(s))
		}
		sort.Strings(req)
		sort.Strings(opt)
		sort.Strings(r.Slots.Required)
		sort.Strings(r.Slots.Optional)
		if strings.Join(req, ",") != strings.Join(r.Slots.Required, ",") ||
			strings.Join(opt, ",") != strings.Join(r.Slots.Optional, ",") {
			t.Errorf("%s: slot row differs from agent.json: %v %v", a, req, opt)
		}
	}
}

func TestShippedLangs(t *testing.T) {
	langs := shippedLangs(t)
	if len(langs) < 3 {
		t.Fatalf("pattern files: %d", len(langs))
	}
	for _, l := range langs {
		if len(l.Extensions) == 0 || len(l.Symbols) == 0 {
			t.Errorf("%s: empty pattern file", l.Name)
		}
	}
}

// TestImportOrder fails when the package imports a package to the left of contextmgr.
func TestImportOrder(t *testing.T) {
	left := []string{"cli", "engine", "intake", "task", "commit", "cleanup", "agent", "llm", "sandbox", "resource"}
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range af.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			for _, l := range left {
				if strings.HasSuffix(p, "/internal/"+l) {
					t.Errorf("%s imports %s", f, p)
				}
			}
		}
	}
}
