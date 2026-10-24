package format

import (
	"errors"
	"io/fs"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"text/template/parse"
)

// ansi matches one colour escape code.
var ansi = regexp.MustCompile("\x1b\\[[0-9;]*m")

const runFacts = `{"group":"tg-1.2","run_id":"0192e4c5-6f00-7a21-8c3b-2d9e0f1a4b57"}`

func TestSameBytes(t *testing.T) {
	f := mustNew(t, Options{Profile: ProfileAdhd, Color: ColorOff})
	a, err := f.FormatJSON(msg("reply.run", "", runFacts))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := f.FormatJSON(msg("reply.run", "", runFacts))
	if a != b {
		t.Fatalf("two runs differ:\n%q\n%q", a.Report, b.Report)
	}
	want := "Run started for `tg-1.2`.\n\n**Run id:** `0192e4c5-6f00-7a21-8c3b-2d9e0f1a4b57`\n"
	if a.Report != want {
		t.Fatalf("report = %q, want %q", a.Report, want)
	}
}

func TestEscaping(t *testing.T) {
	f := mustNew(t, Options{Profile: ProfileAdhd, Color: ColorOff})
	r, err := f.FormatJSON(msg("other", "", `{"path":"a\n## x"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := "Needs a decision; details in `a\\n## x`\n"
	if r.Report != want {
		t.Fatalf("report = %q, want %q", r.Report, want)
	}
	if strings.Count(r.Report, "\n") != 1 {
		t.Fatalf("path reshaped the output into several lines: %q", r.Report)
	}
	for in, want := range map[string]string{
		"plain":  "`plain`",
		"a`b":    "``a`b``",
		"`a":     "`` `a ``",
		"a``b`c": "```a``b`c```",
		"x\r\ny": "`x\\n\\ny`",
		"":       "",
	} {
		if got := span(in); got != want {
			t.Errorf("span(%q) = %q, want %q", in, got, want)
		}
		if got := unspan(span(in)); got != strings.NewReplacer("\r", `\n`, "\n", `\n`).Replace(in) {
			t.Errorf("unspan(span(%q)) = %q", in, got)
		}
	}
}

func TestWrapping(t *testing.T) {
	long := "`" + strings.Repeat("x", 100) + "`"
	got := wrap("see "+long+" now and then some more words to push past the edge", 80)
	lines := strings.Split(got, "\n")
	found := false
	for _, l := range lines {
		if l == long {
			found = true
		}
		if len([]rune(l)) > 80 && l != long {
			t.Errorf("line over 80 columns: %q", l)
		}
	}
	if !found {
		t.Fatalf("long span not alone on a line: %q", got)
	}
	spaced := "a `b c` " + strings.Repeat("w ", 40) + "`d e`"
	for _, l := range strings.Split(wrap(spaced, 80), "\n") {
		if strings.Count(l, "`")%2 != 0 {
			t.Errorf("span split across lines: %q", l)
		}
	}
	table := "| " + strings.Repeat("c ", 60) + "|"
	if wrap(table, 80) != table {
		t.Error("table line was wrapped")
	}
	fenced := "```text\n" + strings.Repeat("word ", 30) + "\n```"
	if wrap(fenced, 80) != fenced {
		t.Error("fenced block was wrapped")
	}
}

func TestTableLimit(t *testing.T) {
	facts := `{"removed":{"head":["group"],"rows":[["a"],["b"],["c"],["d"],["e"],["f"],["g"]],"what":"groups"}}`
	f := mustNew(t, Options{Profile: ProfileAdhd, Color: ColorOff})
	r, err := f.FormatJSON(msg("reply.clean", "", facts))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(r.Report, "\n| `"); n != 5+1 { // 5 rows plus the head
		t.Errorf("table shows %d lines starting with a cell, want 6:\n%s", n, r.Report)
	}
	if !strings.Contains(r.Report, "\n2 more: groups\n") {
		t.Errorf("missing overflow line:\n%s", r.Report)
	}
	if strings.Contains(r.Report, "`f`") {
		t.Errorf("row 6 was printed:\n%s", r.Report)
	}
}

func TestCellLimit(t *testing.T) {
	c45 := strings.Repeat("a", 45)
	c46 := strings.Repeat("b", 46)
	tb := escapedTable(Table{Head: []string{"file", "note"}, Rows: [][]string{{"x", c45}, {"y", c46}}, What: "rows"})
	got, err := tableText(tb)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "`"+c45+"`") {
		t.Errorf("45-character cell moved:\n%s", got)
	}
	if strings.Contains(got, "| `"+c46+"`") {
		t.Errorf("46-character cell stayed in the table:\n%s", got)
	}
	if !strings.Contains(got, "see below") {
		t.Errorf("no placeholder:\n%s", got)
	}
	if !strings.Contains(got, "\n\n`note`, row 2: `"+c46+"`") {
		t.Errorf("no labeled line under the table:\n%s", got)
	}
}

func TestColour(t *testing.T) {
	off := mustNew(t, Options{Profile: ProfileAdhd, Color: ColorOff})
	on := mustNew(t, Options{Profile: ProfileAdhd, Color: ColorOn})
	for _, tc := range []struct{ shape, facts string }{
		{"reply.run", runFacts},
		{"other", `{"path":"/some/long/folder/path/to/a/file.json"}`},
	} {
		a, _ := off.FormatJSON(msg(tc.shape, "", tc.facts))
		b, err := on.FormatJSON(msg(tc.shape, "", tc.facts))
		if err != nil {
			t.Fatal(err)
		}
		if b.Report == a.Report {
			t.Errorf("%s: colour on added nothing", tc.shape)
		}
		if got := ansi.ReplaceAllString(b.Report, ""); got != a.Report {
			t.Errorf("%s: stripped colour %q != colour off %q", tc.shape, got, a.Report)
		}
		if a.Verdict != b.Verdict {
			t.Errorf("%s: verdict changed with colour", tc.shape)
		}
	}
}

func TestConcurrentUse(t *testing.T) {
	f := mustNew(t, Options{Profile: ProfileAdhd, Color: ColorOn})
	want, _ := f.FormatJSON(msg("reply.run", "", runFacts))
	done := make(chan Rendered, 8)
	for i := 0; i < 8; i++ {
		go func() {
			r, _ := f.FormatJSON(msg("reply.run", "", runFacts))
			done <- r
		}()
	}
	for i := 0; i < 8; i++ {
		if got := <-done; got != want {
			t.Errorf("concurrent call differs: %q", got.Report)
		}
	}
}

func TestVerdict(t *testing.T) {
	f := mustNew(t, Options{Profile: ProfileAdhd, Color: ColorOff})
	r, _ := f.FormatJSON(msg("reply.run", "", runFacts))
	if r.Verdict != "Run started for tg-1.2." {
		t.Errorf("verdict = %q", r.Verdict)
	}
	if got := verdictOf("**Bold** and `code` and ``a`b``\nrest"); got != "Bold and code and a`b" {
		t.Errorf("verdictOf = %q", got)
	}
}

func TestUnlistedCase(t *testing.T) {
	for _, p := range []Profile{ProfileAdhd, ProfileStandard} {
		f := mustNew(t, Options{Profile: p, Color: ColorOff})
		r, err := f.FormatJSON(msg("other", "other", `{"path":"/tmp/out.txt"}`))
		if err != nil {
			t.Fatal(err)
		}
		if r.Report != "Needs a decision; details in `/tmp/out.txt`\n" {
			t.Errorf("%s: report = %q", p, r.Report)
		}
	}
}

func TestUnknownInput(t *testing.T) {
	f := mustNew(t, Options{Profile: ProfileAdhd, Color: ColorOff})
	for name, tc := range map[string]struct {
		msg  []byte
		want error
	}{
		"shape":        {msg("reply.nope", "", `{}`), ErrShapeUnknown},
		"shape code":   {msg("error.nope", "nope", `{}`), ErrShapeUnknown},
		"code":         {msg("error.over_limit", "nope", `{}`), ErrCodeUnknown},
		"missing code": {msg("error.over_limit", "", `{}`), ErrCodeUnknown},
		"field":        {msg("reply.run", "", `{"group":"a","extra":1}`), ErrFactsInvalid},
		"type":         {msg("reply.run", "", `{"group":5}`), ErrFactsInvalid},
		"bad code fact": {
			msg("error.over_limit", "over_limit", `{"code":"zzz","reason":"r","fix":"f"}`), ErrCodeUnknown},
	} {
		if _, err := f.FormatJSON(tc.msg); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
	if _, err := f.FormatJSON([]byte(`{not json`)); err == nil {
		t.Error("malformed message accepted")
	}
	if r, err := f.FormatJSON(msg("reply.stop", "", "")); err != nil || !strings.Contains(r.Report, "Run stopped") {
		t.Errorf("missing facts: %v %q", err, r.Report)
	}
}

func TestBannedFunctions(t *testing.T) {
	for _, fn := range []string{"call", "html", "js", "print", "printf", "println", "urlquery"} {
		src := `{{` + fn + ` "x"}}`
		if fn == "call" {
			src = `{{call .F}}`
		}
		fsys := withTemplate(t, "adhd/reply.version.tmpl", src)
		if _, err := newFromFS(fsys, Options{Profile: ProfileAdhd, Color: ColorOff}); err == nil ||
			!strings.Contains(err.Error(), "reply.version") {
			t.Errorf("%s: err = %v, want a failure naming the template", fn, err)
		}
	}
	fsys := withTemplate(t, "adhd/reply.version.tmpl", `{{.Nope`)
	if _, err := newFromFS(fsys, Options{Profile: ProfileAdhd}); err == nil {
		t.Error("template that does not parse accepted")
	}
}

func TestBadOptions(t *testing.T) {
	if _, err := New(Options{Profile: "loud"}); err == nil {
		t.Error("unknown profile accepted")
	}
	if _, err := New(Options{Profile: ProfileAdhd, Color: "auto"}); err == nil {
		t.Error("unresolved colour mode accepted")
	}
}

func TestTurnEnd(t *testing.T) {
	facts := `{"removed":{"head":["item"],"rows":[["a"]],"what":"items"}}`
	on, _ := mustNew(t, Options{Profile: ProfileAdhd, TurnEnd: true}).FormatJSON(msg("reply.clean", "", facts))
	off, _ := mustNew(t, Options{Profile: ProfileAdhd}).FormatJSON(msg("reply.clean", "", facts))
	if !strings.Contains(on.Report, "✅ Successful Changes") || strings.Contains(off.Report, "✅") {
		t.Errorf("turn end headings wrong:\n%s\n---\n%s", on.Report, off.Report)
	}
	empty, _ := mustNew(t, Options{Profile: ProfileAdhd, TurnEnd: true}).FormatJSON(msg("reply.clean", "", `{}`))
	if strings.Contains(empty.Report, "✅") {
		t.Errorf("empty heading printed:\n%s", empty.Report)
	}
}

func TestOutputStyle(t *testing.T) {
	for _, p := range []Profile{ProfileAdhd, ProfileStandard} {
		s, err := OutputStyle(p)
		if err != nil || strings.TrimSpace(s) == "" {
			t.Errorf("OutputStyle(%s) = %q, %v", p, s, err)
		}
	}
	if _, err := OutputStyle("loud"); err == nil {
		t.Error("unknown profile accepted")
	}
}

func TestCodesHaveTemplates(t *testing.T) {
	for _, p := range []Profile{ProfileAdhd, ProfileStandard} {
		for _, c := range AllCodes() {
			shape := "error." + string(c)
			if c == Other {
				shape = "other"
			}
			for _, ext := range []string{".tmpl", ".golden"} {
				if _, err := fs.Stat(outputFS(t), string(p)+"/"+shape+ext); err != nil {
					t.Errorf("code %s: %v", c, err)
				}
			}
		}
	}
	if len(AllCodes()) != 25 {
		t.Errorf("code list has %d entries, want 25", len(AllCodes()))
	}
}

func TestStructAgainstTemplate(t *testing.T) {
	used := map[string]map[string]bool{} // fact struct name -> fields some template reads
	for _, p := range []Profile{ProfileAdhd, ProfileStandard} {
		for _, shape := range shapeNames() {
			info := shapes[shape]
			ptype := reflectType(info.facts())
			src, err := fs.ReadFile(outputFS(t), string(p)+"/"+shape+".tmpl")
			if err != nil {
				t.Fatalf("%s/%s: %v", p, shape, err)
			}
			trees, err := parse0(shape, string(src))
			if err != nil {
				t.Fatalf("%s/%s: %v", p, shape, err)
			}
			fields := jsonFields(ptype)
			if used[ptype.Name()] == nil {
				used[ptype.Name()] = map[string]bool{}
			}
			for _, tr := range trees {
				walkNodes(tr.Root, func(n parse.Node) {
					if fn, ok := n.(*parse.FieldNode); ok {
						if !fields[fn.Ident[0]] {
							t.Errorf("%s/%s reads .%s, which %s lacks", p, shape, fn.Ident[0], ptype.Name())
						}
						used[ptype.Name()][fn.Ident[0]] = true
					}
				})
			}
		}
	}
	for _, shape := range shapeNames() {
		ptype := reflectType(shapes[shape].facts())
		for f := range jsonFields(ptype) {
			if !used[ptype.Name()][f] {
				t.Errorf("no template reads %s.%s", ptype.Name(), f)
			}
		}
	}
}

// mustNew builds a Formatter from the embedded templates.
func mustNew(t *testing.T, o Options) *Formatter {
	t.Helper()
	f, err := New(o)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return f
}

// msg builds a message string from a shape, a code and a facts object.
func msg(shape, code, facts string) []byte {
	m := `{"shape":"` + shape + `"`
	if code != "" {
		m += `,"code":"` + code + `"`
	}
	if facts != "" {
		m += `,"facts":` + facts
	}
	return []byte(m + `}`)
}

// withTemplate copies the embedded output files and replaces one of them.
func withTemplate(t *testing.T, name, text string) fs.FS {
	t.Helper()
	m := fstest.MapFS{}
	err := fs.WalkDir(outputFS(t), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(outputFS(t), p)
		m[p] = &fstest.MapFile{Data: b}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	m[name] = &fstest.MapFile{Data: []byte(text)}
	return m
}
