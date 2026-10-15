package format

import (
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"testing"

	komodo "github.com/rdevitto86/komodo-agentic-harness-coding"
)

func TestControlBytesRenderVisibly(t *testing.T) {
	f := mustNew(t, Options{Profile: ProfileAdhd, Color: ColorOff})
	r, err := f.FormatJSON(msg("other", "", `{"path":"a\u001b[31mred\u0007\tz"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(r.Report, "\x1b\x07") {
		t.Fatalf("raw control byte in %q", r.Report)
	}
	if !strings.Contains(r.Report, `a\x1b[31mred\x07`+"\tz") {
		t.Fatalf("want visible escapes and the tab kept, got %q", r.Report)
	}
}

func TestMismatchedCodeRefused(t *testing.T) {
	f := mustNew(t, Options{Profile: ProfileAdhd, Color: ColorOff})
	errFacts := func(code string) string {
		return `{"code":"` + code + `","reason":"r","fix":"f"}`
	}
	needsFacts := func(code string) string {
		return `{"code":"` + code + `","group":"tg-1.2","fix":"f"}`
	}
	bad := [][]byte{
		msg("error.over_limit", "usage", errFacts("usage")),
		msg("error.over_limit", "over_limit", errFacts("usage")),
		msg("error.over_limit", "", errFacts("over_limit")),
		msg("needs.flap", "returns", needsFacts("returns")),
		msg("needs.flap", "flap", needsFacts("returns")),
		msg("needs.card_changed", "card_changed", needsFacts("card_changed")),
		msg("needs.locked", "locked", needsFacts("locked")),
	}
	for _, m := range bad {
		if _, err := f.FormatJSON(m); !errors.Is(err, ErrCodeUnknown) && !errors.Is(err, ErrShapeUnknown) {
			t.Errorf("%s: err = %v, want a refusal", m, err)
		}
	}
	good := [][]byte{
		msg("error.over_limit", "over_limit", errFacts("over_limit")),
		msg("error.over_limit", "over_limit", `{"reason":"r","fix":"f"}`),
		msg("needs.flap", "flap", needsFacts("flap")),
		msg("error.card_changed", "card_changed", errFacts("card_changed")),
	}
	for _, m := range good {
		if _, err := f.FormatJSON(m); err != nil {
			t.Errorf("%s: %v", m, err)
		}
	}
}

func TestBlockReasonsAreTheNeedsCodes(t *testing.T) {
	if len(BlockReasons()) != 22 {
		t.Fatalf("block reasons: %d, want 22", len(BlockReasons()))
	}
	for _, r := range BlockReasons() {
		if _, ok := shapes["needs."+r]; !ok {
			t.Errorf("no shape needs.%s", r)
		}
		for _, p := range []Profile{ProfileAdhd, ProfileStandard} {
			for _, ext := range []string{".tmpl", ".golden"} {
				if _, err := fs.Stat(outputFS(t), string(p)+"/needs."+r+ext); err != nil {
					t.Errorf("needs.%s: %v", r, err)
				}
			}
		}
		if _, err := fs.Stat(outputFS(t), "fixtures/needs."+r+".json"); err != nil {
			t.Errorf("needs.%s: %v", r, err)
		}
	}
	for _, c := range []string{"card_changed", "locked", "plan_blocked", "plan_input", "plan_timeout", "plan_write", "context_refused"} {
		if _, ok := shapes["needs."+c]; ok {
			t.Errorf("needs.%s is a block reason?", c)
		}
		if _, ok := shapes["error."+c]; !ok {
			t.Errorf("error.%s is not a shape", c)
		}
	}
	f := mustNew(t, Options{Profile: ProfileAdhd, Color: ColorOff})
	if _, err := f.FormatJSON(msg("needs.locked", "locked", `{"group":"g","fix":"f"}`)); !errors.Is(err, ErrShapeUnknown) {
		t.Errorf("needs.locked: %v", err)
	}
}

func TestChainNodeIsWalked(t *testing.T) {
	for _, body := range []string{`{{(printf "%s" .Group).Foo}}`, `{{(call .Group).Foo}}`} {
		fsys := withTemplate(t, "adhd/reply.run.tmpl", "Run "+body+"\n")
		if _, err := newFromFS(fsys, Options{Profile: ProfileAdhd}); err == nil {
			t.Errorf("%s: want New to refuse", body)
		}
	}
}

func TestTableRowWiderThanHeaderFails(t *testing.T) {
	if _, err := tableText(Table{Head: []string{"a", "b"}, Rows: [][]string{{"1", "2"}, {"1", "2", "3"}}}); err == nil {
		t.Fatal("a row with an extra cell rendered")
	}
	f := mustNew(t, Options{Profile: ProfileAdhd, Color: ColorOff})
	facts := `{"rows":{"head":["key"],"rows":[["a","b"]],"what":"keys"}}`
	if _, err := f.FormatJSON(msg("reply.config.get", "", facts)); err == nil {
		t.Fatal("FormatJSON hid an extra cell")
	}
	if _, err := tableText(Table{Rows: [][]string{{"1", "2"}, {"1"}}}); err != nil {
		t.Fatalf("a headless table is sized by its widest row: %v", err)
	}
}

func TestFencedBreaksBacktickRuns(t *testing.T) {
	for in, want := range map[string]string{
		span("plain"):       "plain",
		span("a ``` b"):     "a `` ` b",
		span("`````"):       "`` `` `",
		span("two `` stay"): "two `` stay",
		"":                  "",
	} {
		if got := fnFenced(in); got != want {
			t.Errorf("fnFenced(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReplayCannotCloseItsFence(t *testing.T) {
	for _, p := range []Profile{ProfileAdhd, ProfileStandard} {
		f := mustNew(t, Options{Profile: p, Color: ColorOff})
		facts := `{"run_id":"r","steps":["ok","` + "```" + `\n# injected","\u001b[2Jx"],"gaps":0}`
		r, err := f.FormatJSON(msg("reply.replay", "", facts))
		if err != nil {
			t.Fatal(err)
		}
		fences := 0
		for _, line := range strings.Split(r.Report, "\n") {
			if isFence(line) {
				fences++
			}
		}
		if fences != 2 {
			t.Errorf("%s: %d fence lines, want 2:\n%s", p, fences, r.Report)
		}
		if strings.Contains(r.Report, "\x1b") {
			t.Errorf("%s: raw ESC in the replay", p)
		}
	}
}

func TestEventTimeField(t *testing.T) {
	f := mustNew(t, Options{Profile: ProfileAdhd, Color: ColorOff})
	ok := `{"type":"resource","subtype":"s","run":"r","time":"2026-10-15T10:00:05Z"}`
	if _, err := f.FormatJSON(msg("event.resource", "", ok)); err != nil {
		t.Fatal(err)
	}
	old := `{"type":"resource","subtype":"s","run":"r","at":"2026-10-15T10:00:05Z"}`
	if _, err := f.FormatJSON(msg("event.resource", "", old)); !errors.Is(err, ErrFactsInvalid) {
		t.Fatalf("the at field: %v", err)
	}
}

func TestOverLimitFixtureHoldsTheRealLimit(t *testing.T) {
	b, err := fs.ReadFile(outputFS(t), "fixtures/error.over_limit.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct{ Reason, Fix string }
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	defs, err := komodo.Embedded.ReadFile("embed/defaults.json")
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Keys []struct {
			Name string
			Max  int
		}
	}
	if err := json.Unmarshal(defs, &table); err != nil {
		t.Fatal(err)
	}
	for _, k := range table.Keys {
		if k.Name == "limits.returns" {
			want := "hard limit of " + string(rune('0'+k.Max))
			if !strings.Contains(fx.Reason, want) || !strings.HasSuffix(fx.Fix, " "+string(rune('0'+k.Max))) {
				t.Fatalf("fixture %+v does not hold the limit %d of limits.returns", fx, k.Max)
			}
			return
		}
	}
	t.Fatal("limits.returns not in defaults.json")
}
