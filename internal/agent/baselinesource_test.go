package agent

import (
	"context"
	"strings"
	"testing"
)

// memStore is a baseline store in memory, keyed by tree hash.
type memStore struct {
	byTree map[string]Baseline
	saves  int
}

func (m *memStore) Load(_ context.Context, tree string) (Baseline, bool, error) {
	b, ok := m.byTree[tree]
	return b, ok, nil
}

func (m *memStore) Save(_ context.Context, b Baseline) error {
	if m.byTree == nil {
		m.byTree = map[string]Baseline{}
	}
	m.byTree[b.TreeHash] = b
	m.saves++
	return nil
}

func baseSource(store BaselineStore, lang, junit string) func(context.Context, CommandRunner, string) (Baseline, error) {
	return NewBaselineSource(BaselineConfig{
		Store:    store,
		Tree:     func(context.Context, string) (string, error) { return "tree-base", nil },
		Checkout: func(context.Context, string) (string, error) { return "/work/base", nil },
		Language: lang,
		Test:     "gradle test",
		JUnit:    junit,
	})
}

// The Go baseline runs the whole module's tests in the base checkout, inside Quality Assurance's sandbox, and stores it.
func TestBaselineSourceRunsGoTestsAtTheBaseAndStoresThem(t *testing.T) {
	store := &memStore{}
	sb := newSandbox().on("go test -json", reply{1, jsonTest("m/a", map[string]bool{"TestOld": false, "TestOK": true})})

	b, err := baseSource(store, "go", "")(context.Background(), sb.run, "base1")
	if err != nil {
		t.Fatal(err)
	}
	if b.TreeHash != "tree-base" || b.Passed["m/a.TestOld"] || !b.Passed["m/a.TestOK"] {
		t.Fatalf("baseline = %+v", b)
	}
	if len(sb.ran) != 1 || !strings.HasPrefix(sb.ran[0], "cd '/work/base' && go test -json -count=1 ./...") {
		t.Fatalf("ran = %q", sb.ran)
	}
	if store.saves != 1 || len(store.byTree["tree-base"].Passed) != 2 {
		t.Fatalf("store = %+v", store)
	}
}

// A stored entry for the base tree serves the group: nothing runs.
func TestBaselineSourceReusesAnEntryOfTheSameTree(t *testing.T) {
	store := &memStore{byTree: map[string]Baseline{"tree-base": {TreeHash: "tree-base", Passed: map[string]bool{"x.T": true}}}}
	sb := newSandbox()

	b, err := baseSource(store, "go", "")(context.Background(), sb.run, "base1")
	if err != nil || !b.Passed["x.T"] || len(sb.ran) != 0 {
		t.Fatalf("baseline = %+v %v ran %q", b, err, sb.ran)
	}
}

// A language with a JUnit command reads per-test results from it, after the test command ran in the base checkout.
func TestBaselineSourceReadsTheJUnitReportOfOtherLanguages(t *testing.T) {
	store := &memStore{}
	sb := newSandbox().
		on("gradle test", reply{1, "FAILED"}).
		on("cat results", reply{0, fixture(t, "junit-gradle-a.xml")})

	b, err := baseSource(store, "java", "cat results")(context.Background(), sb.run, "base1")
	if err != nil {
		t.Fatal(err)
	}
	if b.Passed["com.example.CartTest.rejectsNegativeQuantity()"] || !b.Passed["com.example.CartTest.addsItem()"] {
		t.Fatalf("baseline = %+v", b)
	}
	for _, c := range sb.ran {
		if !strings.HasPrefix(c, "cd '/work/base' && ") {
			t.Errorf("command ran outside the base checkout: %q", c)
		}
	}
}

// Without a readable report there are no per-test results: an empty baseline comes back and nothing is stored.
func TestBaselineSourceStoresNothingWithoutPerTestResults(t *testing.T) {
	store := &memStore{}
	sb := newSandbox().on("gradle test", reply{1, "FAILED"})

	b, err := baseSource(store, "java", "")(context.Background(), sb.run, "base1")
	if err != nil || len(b.Passed) != 0 || store.saves != 0 {
		t.Fatalf("baseline = %+v %v saves %d", b, err, store.saves)
	}
}
