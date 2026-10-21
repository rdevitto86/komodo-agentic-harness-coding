package agent

import (
	"strconv"
	"strings"
	"testing"
)

// addDiff builds a unified diff that adds the given lines to a file from line start.
func addDiff(file string, start int, ls ...string) string {
	var b strings.Builder
	b.WriteString("--- a/" + file + "\n+++ b/" + file + "\n")
	b.WriteString("@@ -0,0 +" + itoa(start) + "," + itoa(len(ls)) + " @@\n")
	for _, l := range ls {
		b.WriteString("+" + l + "\n")
	}
	return b.String()
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestLevelBucket(t *testing.T) {
	want := map[FindingLevel]Bucket{
		LevelCritical: BucketBlock,
		LevelHigh:     BucketBlock,
		LevelMedHigh:  BucketBlock,
		LevelMedium:   BucketWarn,
		LevelMedLow:   BucketRecord,
		LevelLow:      BucketRecord,
	}
	for l, b := range want {
		if got := l.Bucket(); got != b {
			t.Errorf("%s bucket = %q, want %q", l, got, b)
		}
	}
}

func TestCatalogRulesMapTheOldSeverities(t *testing.T) {
	cats, err := LoadCatalogs()
	if err != nil {
		t.Fatal(err)
	}
	level := map[string]FindingLevel{}
	for _, c := range cats {
		for _, r := range c.Rules {
			level[r.ID] = r.Level
		}
	}
	want := map[string]FindingLevel{
		"security.1":    LevelCritical, // was blocker
		"concurrency.1": LevelCritical, // was blocker
		"correctness.1": LevelMedHigh,  // was major
		"platform.1":    LevelMedHigh,  // was major
		"quality.1":     LevelMedium,   // was minor
		"platform.2":    LevelMedium,   // was minor
	}
	for id, l := range want {
		if level[id] != l {
			t.Errorf("%s level = %q, want %q", id, level[id], l)
		}
	}
}

// levelReviewer is a Reviewer over one catalog of rules, one per level.
func levelReviewer(t *testing.T) Reviewer {
	t.Helper()
	c := Catalog{Lens: "x", When: WhenAlways}
	for i, l := range []FindingLevel{LevelCritical, LevelHigh, LevelMedHigh, LevelMedium, LevelMedLow, LevelLow} {
		c.Rules = append(c.Rules, Rule{ID: "x." + string(rune('1'+i)), Level: l})
	}
	return newReviewer([]Catalog{c}, nil)
}

func TestAcceptFindingBuckets(t *testing.T) {
	r := levelReviewer(t)
	head := lines{"a.go": {"x := 1", "bad()"}}
	d := Diff{Paths: []string{"a.go"}, Text: addDiff("a.go", 1, "x := 1", "bad()")}
	proof := &Proof{Ran: "go test ./... -run TestBad", Saw: "--- FAIL: TestBad"}

	cases := []struct {
		name  string
		rule  string
		proof *Proof
		conf  int
		want  Accepted
	}{
		{"critical with a proof at 80 blocks", "x.1", proof, 80, Accepted{Level: LevelCritical, Bucket: BucketBlock, In: "head"}},
		{"high with a proof at 100 blocks", "x.2", proof, 100, Accepted{Level: LevelHigh, Bucket: BucketBlock, In: "head"}},
		{"med_high with a proof at 80 blocks", "x.3", proof, 80, Accepted{Level: LevelMedHigh, Bucket: BucketBlock, In: "head"}},
		{"block level at 79 warns", "x.1", proof, 79, Accepted{Level: LevelCritical, Bucket: BucketWarn, In: "head"}},
		{"block level without a proof warns", "x.1", nil, 100, Accepted{Level: LevelCritical, Bucket: BucketWarn, In: "head"}},
		{"a proof naming nothing seen warns", "x.1", &Proof{Ran: "go vet"}, 100,
			Accepted{Level: LevelCritical, Bucket: BucketWarn, In: "head"}},
		{"medium warns at 100", "x.4", proof, 100, Accepted{Level: LevelMedium, Bucket: BucketWarn, In: "head"}},
		{"med_low is recorded", "x.5", proof, 100, Accepted{Level: LevelMedLow, Bucket: BucketRecord, In: "head"}},
		{"low is recorded", "x.6", nil, 0, Accepted{Level: LevelLow, Bucket: BucketRecord, In: "head"}},
	}
	for _, c := range cases {
		f := Finding{Rule: c.rule, File: "a.go", Line: 2, Quote: "bad()", Proof: c.proof, Confidence: c.conf}
		got, err := r.AcceptFinding(f, head, lines{}, d)
		if err != nil || got != c.want {
			t.Errorf("%s: got %+v (err %v), want %+v", c.name, got, err, c.want)
		}
	}
}

// A rule that no command can prove, such as readiness.1, blocks by its level when it carries a proof and confidence 80.
func TestAcceptFindingBlocksAFormerlyUnprovableRule(t *testing.T) {
	cats, err := LoadCatalogs()
	if err != nil {
		t.Fatal(err)
	}
	r := newReviewer(cats, nil)
	head := lines{"a.go": {"bad()"}}
	d := Diff{Paths: []string{"a.go"}, Text: addDiff("a.go", 1, "bad()")}
	f := Finding{
		Rule: "readiness.1", File: "a.go", Line: 1, Quote: "bad()", Confidence: 80,
		Proof: &Proof{Ran: "read the diff", Saw: "no handling of the failure"},
	}

	got, err := r.AcceptFinding(f, head, lines{}, d)
	if err != nil || got.Level != LevelMedHigh || got.Bucket != BucketBlock {
		t.Errorf("readiness.1 = %+v %v, want a block at med_high", got, err)
	}
}

func TestAcceptFindingRefusesConfidenceOutOfRange(t *testing.T) {
	r := levelReviewer(t)
	head := lines{"a.go": {"bad()"}}
	d := Diff{Paths: []string{"a.go"}, Text: addDiff("a.go", 1, "bad()")}

	for _, n := range []int{-1, 101} {
		f := Finding{Rule: "x.1", File: "a.go", Line: 1, Quote: "bad()", Confidence: n}
		if _, err := r.AcceptFinding(f, head, lines{}, d); err == nil {
			t.Errorf("confidence %d accepted", n)
		}
	}
}

func TestAcceptFindingDropsALineTheGroupDidNotChange(t *testing.T) {
	r := levelReviewer(t)
	// the file holds three lines; the group added only line 3
	head := lines{"a.go": {"old()", "older()", "new()"}}
	d := Diff{Paths: []string{"a.go"}, Text: addDiff("a.go", 3, "new()")}
	proof := &Proof{Ran: "go vet ./...", Saw: "x"}

	for _, rule := range []string{"x.1", "x.4", "x.6"} {
		f := Finding{Rule: rule, File: "a.go", Line: 1, Quote: "old()", Proof: proof, Confidence: 100}
		if got, err := r.AcceptFinding(f, head, lines{}, d); err != nil || got.Dropped != DropUnchanged {
			t.Errorf("%s on an unchanged line: %+v %v", rule, got, err)
		}
	}

	// another file's added line 3 does not make a.go line 3 changed
	other := Diff{Paths: []string{"b.go"}, Text: addDiff("b.go", 3, "new()")}
	f := Finding{Rule: "x.4", File: "a.go", Line: 3, Quote: "new()"}
	if got, err := r.AcceptFinding(f, head, lines{}, other); err != nil || got.Dropped != DropUnchanged {
		t.Errorf("another file's change: %+v %v", got, err)
	}

	if got, err := r.AcceptFinding(f, head, lines{}, Diff{}); err != nil || got.Dropped != DropUnchanged {
		t.Errorf("empty diff: %+v %v", got, err)
	}
}
