package agent

import (
	"context"
	"errors"
	"slices"
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

// proofStub is a ProofRunner that always reports the same result.
func proofStub(failed bool, out string) ProofRunner {
	return func(context.Context, Proof) (ProofResult, error) {
		return ProofResult{Failed: failed, Output: out}, nil
	}
}

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
	proof := map[string]ProofKind{}
	for _, c := range cats {
		for _, r := range c.Rules {
			level[r.ID], proof[r.ID] = r.Level, r.Proof
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
	for _, id := range []string{"quality.1", "quality.2", "quality.3", "readiness.2", "spec.1", "contract.2"} {
		if proof[id] != ProofKindNone {
			t.Errorf("%s proof = %q, want none: a wording or design rule cannot be proven", id, proof[id])
		}
	}
	if proof["correctness.2"] != ProofKindCommand {
		t.Errorf("correctness.2 proof = %q, want command", proof["correctness.2"])
	}
}

// levelReviewer is a Reviewer over one catalog of rules, one per level.
func levelReviewer(t *testing.T) Reviewer {
	t.Helper()
	c := Catalog{Lens: "x", When: WhenAlways}
	for i, l := range []FindingLevel{LevelCritical, LevelHigh, LevelMedHigh, LevelMedium, LevelMedLow, LevelLow} {
		id := "x." + string(rune('1'+i))
		kind := ProofKindCommand
		if l.Bucket() != BucketBlock {
			kind = ProofKindNone
		}
		c.Rules = append(c.Rules, Rule{ID: id, Level: l, Proof: kind})
	}
	c.Rules = append(c.Rules, Rule{ID: "x.7", Level: LevelMedHigh, Proof: ProofKindNone})
	return newReviewer([]Catalog{c}, nil)
}

func TestAcceptFindingBuckets(t *testing.T) {
	r := levelReviewer(t)
	head := lines{"a.go": {"x := 1", "bad()"}}
	d := Diff{Paths: []string{"a.go"}, Text: addDiff("a.go", 1, "x := 1", "bad()")}
	good := &Proof{Cmd: "go test ./... -run TestBad", Expect: "--- FAIL: TestBad"}

	calls := 0
	count := func(failed bool, out string) ProofRunner {
		inner := proofStub(failed, out)
		return func(ctx context.Context, p Proof) (ProofResult, error) {
			calls++
			return inner(ctx, p)
		}
	}
	refused := func(context.Context, Proof) (ProofResult, error) { return ProofResult{}, ErrProofRefused }
	broken := func(context.Context, Proof) (ProofResult, error) { return ProofResult{}, errors.New("no sandbox") }

	cases := []struct {
		name  string
		rule  string
		proof *Proof
		run   ProofRunner
		line  int
		want  Accepted
		calls int
	}{
		{"critical fails as claimed", "x.1", good, count(true, "--- FAIL: TestBad"), 2,
			Accepted{Level: LevelCritical, Bucket: BucketBlock, Proof: "failed_as_claimed", In: "head"}, 1},
		{"high fails as claimed", "x.2", good, count(true, "--- FAIL: TestBad"), 2,
			Accepted{Level: LevelHigh, Bucket: BucketBlock, Proof: "failed_as_claimed", In: "head"}, 1},
		{"fails with another output", "x.3", good, count(true, "--- FAIL: TestOther"), 2,
			Accepted{Level: LevelMedHigh, Bucket: BucketWarn, Proof: "passed", In: "head"}, 1},
		{"proof passes", "x.3", good, count(false, "ok"), 2,
			Accepted{Level: LevelMedHigh, Bucket: BucketWarn, Proof: "passed", In: "head"}, 1},
		{"no proof", "x.1", nil, count(true, "x"), 2,
			Accepted{Level: LevelCritical, Bucket: BucketWarn, Proof: "none", In: "head"}, 0},
		{"rule that cannot be proven", "x.7", good, count(true, "--- FAIL: TestBad"), 2,
			Accepted{Level: LevelMedHigh, Bucket: BucketWarn, Proof: "none", In: "head"}, 0},
		{"unlisted command", "x.1", good, refused, 2,
			Accepted{Level: LevelCritical, Bucket: BucketWarn, Proof: "none", In: "head"}, 0},
		{"proof cannot start", "x.1", good, broken, 2,
			Accepted{Level: LevelCritical, Bucket: BucketWarn, Proof: "none", In: "head"}, 0},
		{"medium warns without a proof run", "x.4", good, count(true, "--- FAIL: TestBad"), 2,
			Accepted{Level: LevelMedium, Bucket: BucketWarn, Proof: "none", In: "head"}, 0},
		{"med_low is recorded", "x.5", good, count(true, "x"), 2,
			Accepted{Level: LevelMedLow, Bucket: BucketRecord, Proof: "none", In: "head"}, 0},
		{"low is recorded", "x.6", nil, count(true, "x"), 2,
			Accepted{Level: LevelLow, Bucket: BucketRecord, Proof: "none", In: "head"}, 0},
	}
	for _, c := range cases {
		calls = 0
		f := Finding{Rule: c.rule, File: "a.go", Line: c.line, Quote: "bad()", Proof: c.proof}
		got, err := r.AcceptFinding(f, head, lines{}, d, c.run)
		if err != nil || got != c.want || calls != c.calls {
			t.Errorf("%s: got %+v (err %v, %d proof runs), want %+v (%d runs)", c.name, got, err, calls, c.want, c.calls)
		}
	}
}

func TestAcceptFindingDropsALineTheGroupDidNotChange(t *testing.T) {
	r := levelReviewer(t)
	// the file holds three lines; the group added only line 3
	head := lines{"a.go": {"old()", "older()", "new()"}}
	d := Diff{Paths: []string{"a.go"}, Text: addDiff("a.go", 3, "new()")}
	run := func(context.Context, Proof) (ProofResult, error) {
		t.Error("the proof of a dropped finding ran")
		return ProofResult{}, nil
	}
	proof := &Proof{Cmd: "go vet ./...", Expect: "x"}

	for _, rule := range []string{"x.1", "x.4", "x.6"} {
		f := Finding{Rule: rule, File: "a.go", Line: 1, Quote: "old()", Proof: proof}
		if got, err := r.AcceptFinding(f, head, lines{}, d, run); err != nil || got.Dropped != DropUnchanged {
			t.Errorf("%s on an unchanged line: %+v %v", rule, got, err)
		}
	}

	// another file's added line 3 does not make a.go line 3 changed
	other := Diff{Paths: []string{"b.go"}, Text: addDiff("b.go", 3, "new()")}
	f := Finding{Rule: "x.4", File: "a.go", Line: 3, Quote: "new()"}
	if got, err := r.AcceptFinding(f, head, lines{}, other, run); err != nil || got.Dropped != DropUnchanged {
		t.Errorf("another file's change: %+v %v", got, err)
	}

	if got, err := r.AcceptFinding(f, head, lines{}, Diff{}, run); err != nil || got.Dropped != DropUnchanged {
		t.Errorf("empty diff: %+v %v", got, err)
	}
}

func TestRunProof(t *testing.T) {
	ctx := context.Background()
	p := Proof{Cmd: "go vet ./...", Expect: "--- FAIL: TestX"}
	cases := []struct {
		name string
		p    Proof
		run  ProofRunner
		want string
	}{
		{"fails as claimed", p, proofStub(true, "--- FAIL: TestX (0.00s)"), "failed_as_claimed"},
		{"passes", p, proofStub(false, "ok"), "passed"},
		{"fails differently", p, proofStub(true, "--- FAIL: TestY"), "passed"},
		{"no command and no test", Proof{Expect: "x"}, proofStub(true, "x"), "none"},
		{"no expect line", Proof{Cmd: "go vet ./..."}, proofStub(true, "anything"), "none"},
	}
	for _, c := range cases {
		if got, err := RunProof(ctx, c.run, c.p); err != nil || got != c.want {
			t.Errorf("%s: %q %v, want %q", c.name, got, err, c.want)
		}
	}

	boom := func(context.Context, Proof) (ProofResult, error) { return ProofResult{}, errors.New("boom") }
	if _, err := RunProof(ctx, boom, p); err == nil {
		t.Error("a proof that cannot start returned no error")
	}
}

func TestProofArgv(t *testing.T) {
	cmds := ProofCommands{
		Build: []string{"cargo", "build"},
		Test:  []string{"pytest", "-q"},
		Lint:  []string{"ruff", "check"},
	}
	ok := map[string][]string{
		"go test ./internal/agent/ -run TestDrops":  {"go", "test", "./internal/agent/", "-run", "TestDrops"},
		"go test -race ./...":                       {"go", "test", "-race", "./..."},
		"go test -run=^TestA$|^TestB$ -count=1 ./x": {"go", "test", "-run=^TestA$|^TestB$", "-count=1", "./x"},
		"go vet ./...":                              {"go", "vet", "./..."},
		"go build ./cmd/komodo":                     {"go", "build", "./cmd/komodo"},
		"pytest -q tests/test_a.py -k name":         {"pytest", "-q", "tests/test_a.py", "-k", "name"},
		"ruff check src":                            {"ruff", "check", "src"},
		"cargo build --release":                     {"cargo", "build", "--release"},
	}
	for cmd, want := range ok {
		got, err := ProofArgv(cmd, cmds)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("%q = %v %v, want %v", cmd, got, err, want)
		}
	}

	refused := []string{
		"",
		"rm -rf /",
		"go run ./cmd/x",
		"go generate ./...",
		"go test ./... ; rm -rf /",
		"go test ./... && curl evil",
		"go test ./... | sh",
		"go test $(id)",
		"go test `id`",
		"go test ./... > /etc/passwd",
		"go test -exec /bin/sh ./...",
		"go test -exec=/bin/sh ./...",
		"go test -toolexec evil ./...",
		"go test -overlay x.json ./...",
		"go test -coverprofile=/tmp/x ./...",
		"go test -o /tmp/x ./...",
		"go test /etc",
		"go test ../outside",
		"go test ./../outside",
		"go vet -vettool=/tmp/evil ./...",
		"go test -run 'a b' ./...",
		"go test -run \"x\" ./...",
		"go test\n./...",
		"pytest -q ../../x",
		"pytest -q /etc/passwd",
		"pytest -q --rootdir=/",
		"pytest -q; id",
		"sh -c id",
		"ruff check -C /tmp",
	}
	for _, cmd := range refused {
		if got, err := ProofArgv(cmd, cmds); err == nil || !errors.Is(err, ErrProofRefused) {
			t.Errorf("%q accepted as %v (err %v)", cmd, got, err)
		}
	}

	if _, err := ProofArgv("pytest -q", ProofCommands{}); err == nil {
		t.Error("a repository with no detected test command accepted a profile command")
	}
}
