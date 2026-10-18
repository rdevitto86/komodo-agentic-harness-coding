package commit

import (
	"errors"
	"strings"
	"testing"
)

// CheckMessage refuses a message whose last paragraph is only Name: value lines.
func TestCheckMessage(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		bad  bool
	}{
		{"subject only", "tg-1.2: tsk-1.2.1", false},
		{"label subject", "tg-1.2: Add the lease registry", false},
		{"trailer", "tg-1.2: goal\n\nCo-Authored-By: A <a@b.c>", true},
		{"two trailers", "tg-1.2: goal\n\nSigned-off-by: A\nCo-authored-by: B", true},
		{"trailing newline", "tg-1.2: goal\n\nCo-Authored-By: A <a@b.c>\n", true},
		{"body with summary", "tg-1.2: goal\n\n## Summary\n1 file changed\n\n## Tester\n- 14 tests pass\n", false},
		{"mixed last paragraph", "tg-1.2: goal\n\nNote: x\nplain words", false},
		{"empty", "", true},
	}
	for _, c := range cases {
		err := CheckMessage(c.msg)
		if c.bad != errors.Is(err, ErrTrailer) && c.name != "empty" {
			t.Errorf("%s: err = %v, want trailer error %v", c.name, err, c.bad)
		}
		if c.name == "empty" && err == nil {
			t.Errorf("empty message accepted")
		}
	}
}

// RenderBody fills the group template from the facts and the two verdicts.
func TestRenderBody(t *testing.T) {
	got, err := RenderBody(PRFacts{
		DiffStat:     "6 files changed",
		TasksClosed:  []string{"tsk-1", "tsk-2"},
		FindingCodes: []string{"F-1"},
	},
		Verdict{Summary: []string{"14 tests pass"}}, Verdict{Summary: []string{"0 blocking findings"}})
	if err != nil {
		t.Fatal(err)
	}
	want := "## Summary\n6 files changed\n\n## Tasks closed\n- tsk-1\n- tsk-2\n\n## Findings resolved\n- F-1\n\n## " +
		"Tester\n- 14 tests pass\n\n## Reviewer\n- 0 blocking findings\n"
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

// RenderEpicBody joins the landed groups' sections under the epic's diff stat.
func TestRenderEpicBody(t *testing.T) {
	got, err := RenderEpicBody(EpicFacts{DiffStat: "9 files changed", Groups: []string{"## one\n- a\n", "## two\n- b\n"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "## Summary\n9 files changed\n\n") ||
		strings.Index(got, "## one") > strings.Index(got, "## two") || !strings.Contains(got, "- b") {
		t.Fatalf("got %q", got)
	}
}
