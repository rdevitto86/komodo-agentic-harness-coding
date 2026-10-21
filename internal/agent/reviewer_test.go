package agent

import (
	"slices"
	"strings"
	"testing"
)

func TestReviewerRole(t *testing.T) {
	r := ReviewerRole()
	if r.Name != "reviewer" || !r.PoolSlot || len(r.Tools) != 7 || !slices.Contains(r.Tools, "submit_finding") {
		t.Fatalf("role = %+v", r)
	}
	if !slices.Equal(r.RunClasses, []string{"build", "test", "lint"}) || !slices.Equal(r.Guard, []string{"R1"}) {
		t.Errorf("classes %v guard %v", r.RunClasses, r.Guard)
	}
	if len(r.Slots.Required) != 8 || !slices.Contains(r.Slots.Required, "lens_rules") || len(r.Slots.Optional) != 6 {
		t.Errorf("slots = %+v", r.Slots)
	}
}

func TestCatalogsPassTheFormatChecks(t *testing.T) {
	cats, err := LoadCatalogs()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cats) != len(lensOrder) {
		t.Fatalf("%d catalogs, want %d", len(cats), len(lensOrder))
	}
	minRules := map[string]int{
		"correctness": 6,
		"security":    5,
		"spec":        1,
		"contract":    2,
		"concurrency": 4,
		"performance": 4,
		"platform":    4,
		"config":      3,
		"readiness":   2,
		"quality":     3,
		"economy":     7,
	}
	for _, c := range cats {
		if len(c.Rules) < minRules[c.Lens] {
			t.Errorf("%s has %d rules, want at least %d", c.Lens, len(c.Rules), minRules[c.Lens])
		}
		for _, r := range c.Rules {
			if c.Text[r.ID] == "" {
				t.Errorf("%s has no text", r.ID)
			}
		}
	}
}

func TestCatalogValidateRefusesBadCatalogs(t *testing.T) {
	good := Catalog{
		Lens: "x",
		When: WhenAlways,
		Rules: []Rule{
			{
				ID:       "x.1",
				Level:    LevelMedHigh,
				Proof:    ProofKindCommand,
				Applies:  Applies{Globs: []string{"**"}},
				Check:    "c",
				Evidence: "e",
				Source:   "s",
			},
		},
		Text: map[string]string{"x.1": "t"},
	}
	if err := good.validate(); err != nil {
		t.Fatalf("good catalog: %v", err)
	}
	mut := map[string]func(c *Catalog){
		"wrong id":        func(c *Catalog) { c.Rules[0].ID = "x.2" },
		"foreign lens id": func(c *Catalog) { c.Rules[0].ID = "y.1" },
		"bad level":       func(c *Catalog) { c.Rules[0].Level = "huge" },
		"old severity":    func(c *Catalog) { c.Rules[0].Level = "major" },
		"bad proof kind":  func(c *Catalog) { c.Rules[0].Proof = "magic" },
		"no proof kind":   func(c *Catalog) { c.Rules[0].Proof = "" },
		"no check":        func(c *Catalog) { c.Rules[0].Check = "" },
		"no text":         func(c *Catalog) { c.Text = map[string]string{} },
		"extra text":      func(c *Catalog) { c.Text["x.9"] = "t" },
		"bad when":        func(c *Catalog) { c.When = "sometimes" },
		"bad marker":      func(c *Catalog) { c.Applies.Markers = []string{"("} },
		"no globs":        func(c *Catalog) { c.Rules[0].Applies.Globs = nil },
		"no rules":        func(c *Catalog) { c.Rules = nil },
		"no source":       func(c *Catalog) { c.Rules[0].Source = "" },
	}
	for name, f := range mut {
		c := good
		c.Rules = slices.Clone(good.Rules)
		c.Text = map[string]string{"x.1": "t"}
		f(&c)
		if err := c.validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func newTestReviewer(t *testing.T) Reviewer {
	t.Helper()
	r, err := NewReviewer(func(id string) (FindingLevel, bool) { return LevelMedium, id == "std.comments.1" })
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestLensesMarkdownOnlyDiff(t *testing.T) {
	r := newTestReviewer(t)
	got := r.Lenses(Diff{Paths: []string{"docs/lld.md", "README.md"}, Text: "+text"}, ProfileThorough)
	if !slices.Equal(got, []string{"correctness", "readiness", "quality"}) || len(got) != MarkdownOnlyLenses {
		t.Fatalf("markdown-only lenses = %v", got)
	}
}

func TestLensesSelectByCondition(t *testing.T) {
	r := newTestReviewer(t)
	plain := Diff{Paths: []string{"internal/lease/lock.go"}, Text: "+++ b/internal/lease/lock.go\n+x := 1\n"}
	always := []string{"correctness", "security", "performance", "readiness", "quality"}
	if got := r.Lenses(plain, ProfileThorough); !slices.Equal(got, always) {
		t.Errorf("plain go diff = %v", got)
	}
	cases := []struct {
		name string
		d    Diff
		lens string
	}{
		{
			"exported func",
			Diff{
				Paths: []string{"a.go"},
				Text:  "-func (g TaskGate) Complete(a int) {\n+func (g TaskGate) Complete(a, b int) {\n",
			},
			"contract",
		},
		{"exported type", Diff{Paths: []string{"a.go"}, Text: "+type Role struct {\n"}, "contract"},
		{"goroutine", Diff{Paths: []string{"a.go"}, Text: "+\tgo func() {\n"}, "concurrency"},
		{"channel", Diff{Paths: []string{"a.go"}, Text: "+\tch := make(chan int)\n"}, "concurrency"},
		{"platform file", Diff{Paths: []string{"internal/core/lock_darwin.go"}, Text: "+x\n"}, "platform"},
		{"filepath use", Diff{Paths: []string{"a.go"}, Text: "+\tp := filepath.Join(a, b)\n"}, "platform"},
		{"embedded json", Diff{Paths: []string{"embed/defaults.json"}, Text: "+x\n"}, "config"},
		{"sandbox code", Diff{Paths: []string{"internal/sandbox/run.go"}, Text: "+x\n"}, "security"},
		{"exec code", Diff{Paths: []string{"a.go"}, Text: "+\tcmd := exec.Command(\"ls\") // os/exec\n"}, "security"},
		{"spec cited", Diff{Paths: []string{"a.go"}, Text: "+x\n", Specs: true}, "spec"},
	}
	for _, c := range cases {
		if got := r.Lenses(c.d, ProfileThorough); !slices.Contains(got, c.lens) {
			t.Errorf("%s: lenses %v lack %s", c.name, got, c.lens)
		}
	}
	for _, other := range []string{"contract", "concurrency", "platform", "config", "spec", "economy"} {
		if slices.Contains(r.Lenses(plain, ProfileThorough), other) {
			t.Errorf("plain diff selected %s", other)
		}
	}
	if got := r.Lenses(
		Diff{Paths: []string{"a.go"}, Text: "+++ b/a.go\n--- a/a.go\n+x\n"},
		ProfileThorough,
	); slices.Contains(got, "contract") {
		t.Error("a file header line counted as an exported symbol")
	}
}

func TestLensesEconomyProfile(t *testing.T) {
	r := newTestReviewer(t)
	plain := Diff{Paths: []string{"internal/lease/lock.go"}, Text: "+x := 1\n"}
	if got := r.Lenses(plain, ProfileEconomy); !slices.Equal(got, []string{"security", "economy"}) {
		t.Errorf("economy plain = %v", got)
	}
	sec := Diff{Paths: []string{"internal/sandbox/run.go"}, Text: "+x\n"}
	if got := r.Lenses(sec, ProfileEconomy); !slices.Equal(got, []string{"security", "economy"}) {
		t.Errorf("economy with security = %v", got)
	}
	if got := r.Lenses(sec, ProfileThorough); slices.Contains(got, "economy") {
		t.Errorf("thorough chose economy: %v", got)
	}
}

// lines is a LineSource over a map of file to lines.
type lines map[string][]string

func (l lines) Line(file string, n int) (string, bool) {
	ls := l[file]
	if n < 1 || n > len(ls) {
		return "", false
	}
	return ls[n-1], true
}

func TestVerifyFinding(t *testing.T) {
	head := lines{"a.go": {"package a", "\tout, _ := g.Run(ctx, c)", "x"}}
	diff := lines{"b.go": {"+new line"}}
	f := Finding{Rule: "correctness.2", File: "a.go", Line: 2, Quote: "out, _ := g.Run(ctx, c)"}
	if in, ok := VerifyFinding(f, head, diff); !ok || in != "head" {
		t.Errorf("leading whitespace: %q %v", in, ok)
	}
	if in, ok := VerifyFinding(Finding{File: "b.go", Line: 1, Quote: "+new line"}, head, diff); !ok || in != "diff" {
		t.Errorf("diff source: %q %v", in, ok)
	}
	bad := []Finding{
		{File: "a.go", Line: 2, Quote: "OUT, _ := g.Run(ctx, c)"},
		{File: "a.go", Line: 2, Quote: "out,  _ := g.Run(ctx, c)"},
		{File: "a.go", Line: 3, Quote: "out, _ := g.Run(ctx, c)"},
		{File: "a.go", Line: 2, Quote: ""},
		{File: "a.go", Line: 2, Quote: "   "},
		{File: "a.go", Line: 1, Quote: "package a\n\tout"},
		{File: "nope.go", Line: 1, Quote: "package a"},
	}
	for _, b := range bad {
		if _, ok := VerifyFinding(b, head, diff); ok {
			t.Errorf("verified %+v", b)
		}
	}
	if _, ok := VerifyFinding(f, nil, nil); ok {
		t.Error("nil sources verified a finding")
	}
}

func TestAcceptFinding(t *testing.T) {
	r := newTestReviewer(t)
	head := lines{"a.go": {"x := 1", "bad()"}}
	d := Diff{Paths: []string{"a.go"}, Text: addDiff("a.go", 1, "x := 1", "bad()")}
	run := proofStub(true, "--- FAIL: TestBad")
	proof := &Proof{Cmd: "go test ./... -run TestBad", Expect: "--- FAIL: TestBad"}
	ok := Finding{Rule: "correctness.1", File: "a.go", Line: 2, Quote: "bad()", Proof: proof}

	got, err := r.AcceptFinding(ok, head, lines{}, d, run)
	if err != nil || got.Dropped != "" || got.Level != LevelMedHigh || got.Bucket != BucketBlock ||
		got.Proof != "failed_as_claimed" {
		t.Errorf("verified finding: %+v %v", got, err)
	}

	minor := Finding{Rule: "quality.1", File: "a.go", Line: 2, Quote: "bad()"}
	if got, err := r.AcceptFinding(minor, head, lines{}, d, run); err != nil || got.Level != LevelMedium ||
		got.Bucket != BucketWarn {
		t.Errorf("medium finding: %+v %v", got, err)
	}

	fake := Finding{Rule: "correctness.1", File: "a.go", Line: 2, Quote: "made up", Proof: proof}
	if got, err := r.AcceptFinding(fake, head, lines{}, d, run); err != nil || got.Dropped != DropQuote {
		t.Errorf("fake quote: %+v %v", got, err)
	}

	unknown := Finding{Rule: "x.99", File: "a.go", Line: 1, Quote: "x := 1"}
	if _, err := r.AcceptFinding(unknown, head, lines{}, d, run); err == nil {
		t.Error("unknown rule accepted")
	}

	std := Finding{Rule: "std.comments.1", File: "a.go", Line: 1, Quote: "x := 1"}
	if got, err := r.AcceptFinding(std, head, lines{}, d, run); err != nil || got.Dropped != "" || got.Bucket != BucketWarn {
		t.Errorf("standards rule: %+v %v", got, err)
	}

	for _, shape := range []Finding{
		{Rule: "correctness.1", Line: 1, Quote: "q"},
		{Rule: "correctness.1", File: "a.go", Line: 0, Quote: "q"},
	} {
		if _, err := r.AcceptFinding(shape, head, lines{}, d, run); err == nil {
			t.Errorf("bad shape accepted: %+v", shape)
		}
	}
}

func TestAcceptFindingTakesAPerformanceRule(t *testing.T) {
	r := newTestReviewer(t)
	head := lines{"a.go": {"for _, id := range ids {", "\tb, _ := os.ReadFile(path)"}}
	d := Diff{Paths: []string{"a.go"}, Text: addDiff("a.go", 1, "for _, id := range ids {", "\tb, _ := os.ReadFile(path)")}
	f := Finding{
		Rule: "performance.2", File: "a.go", Line: 2, Quote: "b, _ := os.ReadFile(path)",
		Proof: &Proof{Cmd: "go test ./... -run TestWhole", Expect: "--- FAIL: TestWhole"},
	}
	got, err := r.AcceptFinding(f, head, lines{}, d, proofStub(true, "--- FAIL: TestWhole"))
	if err != nil || got.Level != LevelMedHigh || got.Bucket != BucketBlock {
		t.Errorf("performance finding: %+v %v", got, err)
	}
}

func TestExactlyOnce(t *testing.T) {
	if err := ExactlyOnce([]string{"a", "b"}, []string{"b", "a"}); err != nil {
		t.Errorf("exact list: %v", err)
	}
	if err := ExactlyOnce([]string{"a", "b"}, []string{"a"}); err == nil || !strings.Contains(err.Error(), "b") {
		t.Errorf("skipped: %v", err)
	}
	if err := ExactlyOnce([]string{"a", "b"}, []string{"a", "b", "a"}); err == nil ||
		!strings.Contains(err.Error(), "a has 2") {
		t.Errorf("repeated: %v", err)
	}
}

func TestReviewerResult(t *testing.T) {
	raw := `{"verdicts": [{"task": "tsk-add-lock", "verdict": "met"}, {"task": "tsk-add-scope", "verdict": "not_met"}]}`
	r, err := ParseReviewerResult([]byte(raw))
	if err != nil || len(r.Verdicts) != 2 {
		t.Fatalf("parse: %+v %v", r, err)
	}
	rv := newTestReviewer(t)
	if err := rv.AcceptResult([]string{"tsk-add-lock", "tsk-add-scope"}, r); err != nil {
		t.Errorf("accept: %v", err)
	}
	if err := rv.AcceptResult([]string{"tsk-add-lock", "tsk-add-scope", "tsk-x"}, r); err == nil {
		t.Error("skipped task accepted")
	}
	r.Verdicts[1].Task = "tsk-add-lock"
	if err := rv.AcceptResult([]string{"tsk-add-lock", "tsk-add-scope"}, r); err == nil {
		t.Error("repeated task accepted")
	}
	for _, bad := range []string{
		`{"verdicts": [{"task": "t", "verdict": "ok"}]}`,
		`{"verdicts": [], "x": 1}`,
		`{"verdicts": [{"task": "", "verdict": "met"}]}`,
	} {
		if _, err := ParseReviewerResult([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestPrecisionVerifiedPct(t *testing.T) {
	if got := (Precision{Rule: "r", Submitted: 10, Verified: 3}).VerifiedPct(); got != 30 {
		t.Errorf("pct = %v", got)
	}
	if got := (Precision{}).VerifiedPct(); got != 0 {
		t.Errorf("empty pct = %v", got)
	}
}

func TestDecide(t *testing.T) {
	pass := QAVerdict{Ended: true, Pass: true}
	rpass := ReviewerVerdict{Ended: true}
	blocking := []Finding{{Rule: "correctness.1", File: "a.go", Line: 2, Quote: "q"}}
	cases := []struct {
		name   string
		t      QAVerdict
		r      ReviewerVerdict
		want   Decision
		reason string
	}{
		{"both pass", pass, rpass, DecisionApprove, ""},
		{"reviewer still running", pass, ReviewerVerdict{}, DecisionWait, ""},
		{"Quality Assurance still running", QAVerdict{}, rpass, DecisionWait, ""},
		{
			"reviewer fails first, Quality Assurance cancelled",
			QAVerdict{},
			ReviewerVerdict{Ended: true, Blocking: blocking},
			DecisionReturn,
			"",
		},
		{"reviewer not_met", pass, ReviewerVerdict{Ended: true, NotMet: []string{"t1"}}, DecisionReturn, ""},
		{
			"Quality Assurance item failed, reviewer done",
			QAVerdict{Ended: true, Failed: []string{"ac-1"}},
			rpass,
			DecisionReturn,
			"",
		},
		{
			"Quality Assurance item failed, reviewer running",
			QAVerdict{Ended: true, Failed: []string{"ac-1"}},
			ReviewerVerdict{},
			DecisionWait,
			"",
		},
		{"Quality Assurance task not confirmed", QAVerdict{Ended: true, NotMet: []string{"t1"}}, rpass, DecisionReturn, ""},
		{"first dispute", QAVerdict{Ended: true, Disputed: []string{"ac-1"}}, rpass, DecisionWait, ""},
		{
			"first dispute while reviewer fails",
			QAVerdict{Ended: true, Disputed: []string{"ac-1"}},
			ReviewerVerdict{Ended: true, Blocking: blocking},
			DecisionReturn,
			"",
		},
		{
			"second dispute",
			QAVerdict{Ended: true, Restarts: 1, Disputed: []string{"ac-1"}},
			ReviewerVerdict{},
			DecisionBlock,
			BlockQA,
		},
		{"split", QAVerdict{Ended: true, Split: []string{"ac-1"}}, ReviewerVerdict{}, DecisionBlock, BlockFlaky},
		{"brief incomplete", pass, ReviewerVerdict{Ended: true, BriefIncomplete: true}, DecisionBlock, BlockBrief},
		{
			"unconfirmed item never blocks",
			QAVerdict{Ended: true, Pass: true, Skipped: []string{"ac-9"}},
			rpass,
			DecisionApprove,
			"",
		},
		{"Quality Assurance not passed with no detail", QAVerdict{Ended: true}, rpass, DecisionReturn, ""},
	}
	for _, c := range cases {
		got := Decide(c.t, c.r)
		if got.Decision != c.want || got.Reason != c.reason {
			t.Errorf("%s: %s %q, want %s %q", c.name, got.Decision, got.Reason, c.want, c.reason)
		}
	}
	out := Decide(QAVerdict{Ended: true, Failed: []string{"ac-1"}, NotMet: []string{"t2", "t1"}}, ReviewerVerdict{
		Ended:    true,
		Blocking: blocking,
		NotMet:   []string{"t1"},
	})
	if out.Decision != DecisionReturn || len(out.Findings) != 1 || !slices.Equal(out.Items, []string{"ac-1"}) ||
		!slices.Equal(out.NotMet, []string{"t1", "t2"}) {
		t.Errorf("both fail in one tick: %+v", out)
	}
}

func TestSecurityAppliesToADiffInAnyPath(t *testing.T) {
	r := newTestReviewer(t)
	for _, p := range []string{"a.go", "src/main/App.java", "pkg/deep/er/x.py", "internal/lease/lock.go"} {
		d := Diff{Paths: []string{p}, Text: "+++ b/" + p + "\n+x := 1\n"}
		for _, prof := range []Profile{ProfileThorough, ProfileEconomy} {
			if got := r.Lenses(d, prof); !slices.Contains(got, "security") {
				t.Errorf("%s under %s: lenses %v lack security", p, prof, got)
			}
		}
	}
}

func TestLensGlobsNameNoRepositoryPath(t *testing.T) {
	cats, err := LoadCatalogs()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cats {
		for _, g := range c.Applies.Globs {
			if !strings.HasPrefix(g, "**/") && !strings.HasPrefix(g, "*") {
				t.Errorf("lens %s glob %q is anchored to a repository path", c.Lens, g)
			}
		}
	}
}

func TestCatalogsCarryTheNewRules(t *testing.T) {
	cats, err := LoadCatalogs()
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]FindingLevel{}
	for _, c := range cats {
		for _, r := range c.Rules {
			ids[r.ID] = r.Level
		}
		if c.Lens == "performance" && c.When != WhenAlways {
			t.Errorf("performance applies %q, want always", c.When)
		}
		if c.Lens == "security" && c.When != WhenAlways {
			t.Errorf("security applies %q, want always", c.When)
		}
	}
	for _, id := range []string{
		"performance.1", "performance.2", "performance.3", "performance.4", "correctness.5", "correctness.6",
	} {
		if _, ok := ids[id]; !ok {
			t.Errorf("rule %s is missing", id)
		}
	}
}

func TestEveryRuleHasFullText(t *testing.T) {
	cats, err := LoadCatalogs()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cats {
		for _, r := range c.Rules {
			if n := len(strings.Fields(c.Text[r.ID])); n < 20 {
				t.Errorf("%s text has %d words, want at least 20", r.ID, n)
			}
		}
	}
}
