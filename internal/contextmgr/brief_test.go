package contextmgr

import (
	"errors"
	"strings"
	"testing"
)

const builderTpl = "# Role\n\nYou are the builder.\n\n```\n# Brief\n## Errors\n```\n\n# " +
	"Brief\n\n{{repo_profile}}\n{{group}}\n{{tasks}}\n{{files}}\n{{file_contents}}\n{{diff}}\n{{findings}}\n"

func TestParseTemplate(t *testing.T) {
	tpl, err := ParseTemplate("builder", builderTpl)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !strings.HasPrefix(tpl.Role, "# Role\n") || !strings.Contains(tpl.Role, "# Brief\n## Errors") ||
		strings.Contains(tpl.Role, "{{") {
		t.Fatalf("role: %q", tpl.Role)
	}
	if !strings.HasPrefix(tpl.Brief, "# Brief\n") || len(tpl.Slots) != 7 || tpl.Slots[0] != RepoProfile {
		t.Fatalf("brief: %q slots %v", tpl.Brief, tpl.Slots)
	}
	bad := map[string]string{
		"no brief":         "# Role\n\nText\n",
		"unknown slot":     strings.Replace(builderTpl, "{{diff}}", "{{nope}}", 1),
		"slot not in row":  strings.Replace(builderTpl, "{{diff}}", "{{lens}}", 1),
		"required missing": strings.Replace(builderTpl, "{{tasks}}\n", "", 1),
		"slot in role":     strings.Replace(builderTpl, "You are the builder.", "You are {{group}}.", 1),
		"no role":          "# Brief\n\n{{repo_profile}}\n",
	}
	for name, text := range bad {
		if _, err := ParseTemplate("builder", text); !errors.Is(err, ErrTemplate) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	if _, err := ParseTemplate("nobody", builderTpl); !errors.Is(err, ErrAgentUnknown) {
		t.Errorf("unknown agent: %v", err)
	}
}

func mustTpl(t *testing.T) Template {
	t.Helper()
	tpl, err := ParseTemplate("builder", builderTpl)
	if err != nil {
		t.Fatal(err)
	}
	return tpl
}

func builderVals() Values {
	return Values{RepoProfile: "profile", Group: "tg-1", Tasks: "tasks", Files: "a.go", FileContents: "package a"}
}

func TestBuildBriefParts(t *testing.T) {
	br, err := buildBrief("builder", mustTpl(t), "TOOLS", "GUIDE", "STANDARDS", builderVals())
	if err != nil {
		t.Fatal(err)
	}
	if br.Agent != "builder" || br.Parts[0] != "TOOLS" || br.Parts[2] != "GUIDE" || br.Parts[3] != "STANDARDS" {
		t.Fatalf("parts %q", br.Parts)
	}
	if !strings.HasPrefix(br.Parts[1], "# Role") || !strings.HasPrefix(br.Parts[4], "# Brief") {
		t.Fatalf("role/task parts %q", br.Parts)
	}
	if !strings.Contains(br.Parts[4], "profile\ntg-1\ntasks\na.go\npackage a\nnone\nnone\n") {
		t.Fatalf("task part: %q", br.Parts[4])
	}
}

func TestBuildBriefEmptyRequired(t *testing.T) {
	vals := builderVals()
	vals[Tasks] = "  \n"
	_, err := buildBrief("builder", mustTpl(t), "", "", "", vals)
	var e EmptySlotError
	if !errors.As(err, &e) || e.Slot != Tasks || !strings.Contains(err.Error(), "tasks") {
		t.Fatalf("got %v", err)
	}
}

func TestBuildBriefOnePass(t *testing.T) {
	vals := builderVals()
	vals[Group] = "keep {{task}} and {{tasks}} literal"
	br, err := buildBrief("builder", mustTpl(t), "", "", "", vals)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(br.Parts[4], "keep {{task}} and {{tasks}} literal\ntasks\n") {
		t.Fatalf("task part: %q", br.Parts[4])
	}
}

func TestBuildBriefDropsForeignSlots(t *testing.T) {
	vals := builderVals()
	vals[Lens] = "REVIEWER-ONLY"
	vals[Slot("summary")] = "BUILDER-SUMMARY"
	br, _ := buildBrief("builder", mustTpl(t), "", "", "", vals)
	if strings.Contains(br.Text(), "REVIEWER-ONLY") || strings.Contains(br.Text(), "BUILDER-SUMMARY") {
		t.Fatal("a slot the agent's row does not name reached the brief")
	}
}

func TestTokensAndPlace(t *testing.T) {
	br := Brief{Parts: [5]string{strings.Repeat("a", 40), strings.Repeat("b", 40), "", strings.Repeat("c", 20), ""}}
	if got := br.Tokens(CountTokens); got != 25 {
		t.Fatalf("tokens %d", got)
	}
	if Ceiling(51) != 25 {
		t.Fatal("ceiling is half the context, rounded down")
	}
	chain := []Candidate{{"small", 40}, {"fits", 50}, {"huge", 1000}}
	got, err := br.Place(chain, nil)
	if err != nil || got.Link != "fits" {
		t.Fatalf("place: %v %v", got, err)
	}
	before := br
	if _, err := br.Place([]Candidate{{"a", 10}, {"b", 49}}, CountTokens); !errors.Is(err, ErrContext) {
		t.Fatalf("over every ceiling: %v", err)
	}
	if br != before {
		t.Fatal("Place must not cut the brief")
	}
	if _, err := br.Place(nil, nil); !errors.Is(err, ErrContext) {
		t.Fatalf("empty chain: %v", err)
	}
}

func TestBriefText(t *testing.T) {
	br := Brief{Parts: [5]string{"one", "two", "", "four", "five"}}
	if got := br.Text(); got != "one\n\ntwo\n\nfour\n\nfive" {
		t.Fatalf("got %q", got)
	}
}

func TestCheckSkillSize(t *testing.T) {
	if err := checkSkillSize(CountTokens, strings.Repeat("a", 6000*4)); err != nil {
		t.Fatalf("6,000 tokens must pass: %v", err)
	}
	if err := checkSkillSize(CountTokens, strings.Repeat("a", 6000*4+1)); err == nil {
		t.Fatal("6,001 tokens must fail")
	}
}
