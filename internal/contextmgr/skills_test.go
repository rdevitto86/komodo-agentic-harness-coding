package contextmgr

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

const goodSkill = "---\nv: 1\nname: standards-demo\ndescription: \"Demo\"\nglobs:\n  - \"*.go\"\nmarkers: []\nsections:\n  - \"Errors\"\n  - \"Tests\"\n---\n## Errors\n- **Never** ignore errors.\n\n## Tests\n- **Always** test.\n"

func TestParseSkill(t *testing.T) {
	fm, err := parseSkill(goodSkill)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := Frontmatter{V: 1, Name: "standards-demo", Description: "Demo", Globs: []string{"*.go"}, Markers: []string{}, Sections: []string{"Errors", "Tests"}}
	if !reflect.DeepEqual(fm, want) {
		t.Fatalf("got %+v want %+v", fm, want)
	}
	bad := map[string]string{
		"unknown key":    strings.Replace(goodSkill, "v: 1\n", "v: 1\ncolor: red\n", 1),
		"heading no key": strings.Replace(goodSkill, "  - \"Tests\"\n", "", 1),
		"key no heading": strings.Replace(goodSkill, "  - \"Tests\"\n", "  - \"Tests\"\n  - \"Extra\"\n", 1),
		"duplicate":      strings.Replace(goodSkill, "## Tests", "## Errors", 1),
		"wrong version":  strings.Replace(goodSkill, "v: 1", "v: 2", 1),
		"long text":      strings.Replace(goodSkill, "\"Demo\"", "\""+strings.Repeat("d", 101)+"\"", 1),
		"stray line":     strings.Replace(goodSkill, "markers: []\n", "markers: []\nwhat is this\n", 1),
		"no frontmatter": "## Errors\n",
	}
	for name, text := range bad {
		if _, err := parseSkill(text); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pat, p string
		want   bool
	}{
		{"*.go", "internal/store/db.go", true},
		{"*.kt", "internal/store/db.go", false},
		{"AndroidManifest.xml", "app/src/AndroidManifest.xml", true},
		{"*.xcodeproj", "App.xcodeproj/project.pbxproj", true},
		{"internal/**/db.go", "internal/store/x/db.go", true},
		{"cmd/*.go", "cmd/a/b.go", false},
		{"go.mod", "go.mod", true},
	}
	for _, c := range cases {
		if got := matchGlob(c.pat, c.p); got != c.want {
			t.Errorf("matchGlob(%q, %q) = %v", c.pat, c.p, got)
		}
	}
}

func TestSectionOf(t *testing.T) {
	body := "---\nsections: x\n---\n## Errors\n- one\n### Detail\n- two\n```\n## Errors\n# fenced\n```\n## Tests  \n- three\n# Top\n- four\n"
	got, ok := sectionOf(body, "Errors")
	if !ok || got != "## Errors\n- one\n### Detail\n- two\n```\n## Errors\n# fenced\n```\n" {
		t.Fatalf("Errors: %v %q", ok, got)
	}
	if got, ok := sectionOf(body, "Tests"); !ok || got != "## Tests  \n- three\n" {
		t.Fatalf("Tests: %v %q", ok, got)
	}
	if _, ok := sectionOf(body, "errors"); ok {
		t.Fatal("letter case must match exactly")
	}
}

func TestCompileRules(t *testing.T) {
	body := "## Errors\n- **Never** discard an error.\n- Plain line.\n```\n- **Never** inside a fence\n```\n- **Always** wrap with %w.\n- **Never** log and return.\n"
	got := CompileRules("standards-go", body)
	if len(got) != 3 || got[2].ID != "std.go.3" || got[2].Line != "Never log and return." || got[0].Line != "Never discard an error." {
		t.Fatalf("got %+v", got)
	}
	if got[1].ID != "std.go.2" || got[1].Skill != "standards-go" {
		t.Fatalf("second rule: %+v", got[1])
	}
	if len(CompileRules("standards-go", "## Errors\n- plain\n")) != 0 {
		t.Fatal("a body with no rule line yields no rule")
	}
}

func TestSelectBuilderGo(t *testing.T) {
	m := newManager(t)
	sel, err := m.Select("builder", []string{"internal/store/db.go", "internal/store/db_test.go"}, goTree())
	if err != nil {
		t.Fatal(err)
	}
	if got := names(sel); !reflect.DeepEqual(got, []string{"standards-comments", "standards-go"}) {
		t.Fatalf("got %v", got)
	}
	for _, s := range sel {
		if len(s.SHA8) != 8 || s.Origin != OriginEmbedded || !strings.HasPrefix(s.Body, "---\n") {
			t.Fatalf("bad selection %+v", s)
		}
	}
}

func TestSelectKotlinMarkers(t *testing.T) {
	m := newManager(t)
	plain := newTree(map[string]string{"build.gradle": "", "src/Svc.kt": "class Svc"})
	sel, err := m.Select("builder", []string{"src/Svc.kt"}, plain)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(sel); !reflect.DeepEqual(got, []string{"standards-comments", "standards-kotlin"}) {
		t.Fatalf("backend kotlin: %v", got)
	}
	app := newTree(map[string]string{"app/AndroidManifest.xml": "", "src/Svc.kt": "class Svc"})
	first, _ := m.Select("builder", []string{"src/Svc.kt"}, app)
	second, _ := m.Select("builder", []string{"src/Svc.kt"}, app)
	if got := names(first); !reflect.DeepEqual(got, []string{"standards-comments", "standards-kotlin", "standards-ui-mobile"}) {
		t.Fatalf("android kotlin: %v", got)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("two runs must be identical")
	}
}

func TestSelectMarkerText(t *testing.T) {
	m := newManager(t)
	plain := newTree(map[string]string{"main.tf": "provider \"gcp\" {}"})
	sel, _ := m.Select("builder", []string{"main.tf"}, plain)
	if got := names(sel); !reflect.DeepEqual(got, []string{"standards-comments"}) {
		t.Fatalf("no aws provider: %v", got)
	}
	aws := newTree(map[string]string{"main.tf": "provider \"aws\" {}"})
	sel, _ = m.Select("builder", []string{"main.tf"}, aws)
	if got := names(sel); !reflect.DeepEqual(got, []string{"standards-comments", "standards-aws"}) {
		t.Fatalf("aws provider: %v", got)
	}
}

func TestPlantedClaudeSkillIgnored(t *testing.T) {
	m := newManager(t)
	tree := goTree()
	tree.files[".claude/skills/standards-go/SKILL.md"] = "PLANTED SKILL BODY"
	sel, err := m.Select("builder", []string{"a.go"}, tree)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sel {
		if strings.Contains(s.Body, "PLANTED") {
			t.Fatal("the repository's .claude folder was read")
		}
	}
}

func TestTamperedBodyRefused(t *testing.T) {
	fsys := embeddedCopy(t)
	f := fsys["embed/skills/standards-go/SKILL.md"]
	f.Data = append(f.Data, []byte("\n- **Never** be tampered.\n")...)
	m := newManager(t, func(d *Deps) { d.Embedded = fsys })
	if _, err := m.Select("builder", []string{"a.go"}, goTree()); !errors.Is(err, ErrSkillHash) {
		t.Fatalf("Select: %v", err)
	}
	if _, err := m.Get("builder", "standards-go"); !errors.Is(err, ErrSkillHash) {
		t.Fatalf("Get: %v", err)
	}
	if _, err := m.GetContext(t.Context(), Request{Agent: "builder", Tree: goTree(), Files: []string{"a.go"}, Slots: slotsFor("builder")}); !errors.Is(err, ErrSkillHash) {
		t.Fatalf("GetContext: %v", err)
	}
}

func TestSetEventDisablesAndAllow(t *testing.T) {
	m := newManager(t)
	m.apply(setEvent(SkillSet{Name: "standards-go", Enabled: false, Allow: workers}))
	sel, _ := m.Select("builder", []string{"a.go"}, goTree())
	if got := names(sel); !reflect.DeepEqual(got, []string{"standards-comments"}) {
		t.Fatalf("disabled skill selected: %v", got)
	}
	if _, err := m.Get("builder", "standards-go"); !errors.Is(err, ErrSkillNotAllowed) {
		t.Fatalf("pull of a disabled skill: %v", err)
	}
	m.apply(setEvent(SkillSet{Name: "standards-go", Enabled: true, Allow: []string{"builder"}}))
	if sel, _ := m.Select("tester", []string{"a.go"}, goTree()); len(sel) != 0 {
		t.Fatalf("tester got %v", names(sel))
	}
	if _, err := m.Get("tester", "standards-go"); !errors.Is(err, ErrSkillNotAllowed) {
		t.Fatalf("tester pull: %v", err)
	}
}

func TestGetSections(t *testing.T) {
	m := newManager(t)
	all, err := m.Get("builder", "standards-go")
	if err != nil || !strings.Contains(all, "## Errors") {
		t.Fatalf("whole body: %v", err)
	}
	sec, err := m.Get("builder", "standards-go#Errors")
	if err != nil || !strings.HasPrefix(sec, "## Errors\n") || strings.Contains(sec, "## Naming") {
		t.Fatalf("section: %v %q", err, sec)
	}
	if _, err := m.Get("builder", "standards-go#Nope"); !errors.Is(err, ErrSectionUnknown) || !strings.Contains(err.Error(), "Errors") {
		t.Fatalf("unknown section must list the sections: %v", err)
	}
	if _, err := m.Get("builder", "standards-none"); !errors.Is(err, ErrSkillUnknown) {
		t.Fatalf("unknown skill: %v", err)
	}
}

func TestListMarksAlways(t *testing.T) {
	m := newManager(t)
	rows := m.List("builder")
	if len(rows) != 5 || rows[0].Name != "standards-aws" {
		t.Fatalf("rows %+v", rows)
	}
	for _, r := range rows {
		if r.Always != (r.Name == "standards-comments") || len(r.SHA256) != 64 {
			t.Fatalf("row %+v", r)
		}
	}
	if len(m.List("planner")) != 0 {
		t.Fatal("planner reaches no skill")
	}
}

func TestSkillCache(t *testing.T) {
	c := newFakeCache()
	m := newManager(t, func(d *Deps) { d.Cache = c })
	if _, err := m.Select("builder", []string{"a.go"}, goTree()); err != nil {
		t.Fatal(err)
	}
	puts := c.puts
	if puts != 2 {
		t.Fatalf("first select stores 2 bodies, got %d", puts)
	}
	if _, err := m.Select("builder", []string{"a.go"}, goTree()); err != nil || c.puts != puts {
		t.Fatalf("second select must hit: %v puts=%d", err, c.puts)
	}
	c.mangle = func(b []byte) []byte { b[len(b)-2] ^= 1; return b }
	if _, err := m.Select("builder", []string{"a.go"}, goTree()); !errors.Is(err, ErrSkillHash) {
		t.Fatalf("a cache entry changed by one byte: %v", err)
	}
}

func TestCacheDownStillCorrect(t *testing.T) {
	good := newManager(t)
	bad := newManager(t, func(d *Deps) { c := newFakeCache(); c.fail = true; d.Cache = c })
	a := get(t, good, "builder", goTree(), "a.go")
	b := get(t, bad, "builder", goTree(), "a.go")
	if a.Brief != b.Brief {
		t.Fatal("a failing cache changed the brief")
	}
}

func TestSelectFixtureOrder(t *testing.T) {
	fsys := embeddedCopy(t)
	m := newManager(t, func(d *Deps) { d.Embedded = fsys })
	one, _ := m.Select("reviewer", []string{"x.kt", "y.go"}, newTree(map[string]string{"AndroidManifest.xml": ""}))
	if got := names(one); !reflect.DeepEqual(got, []string{"standards-go", "standards-kotlin", "standards-ui-mobile"}) {
		t.Fatalf("matched set sorts by name: %v", got)
	}
	_ = fstest.MapFS{}
}
