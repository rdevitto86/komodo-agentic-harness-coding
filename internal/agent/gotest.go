package agent

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

// TestRun is the result of one test: a Go test of a package, or a whole test command for other languages.
type TestRun struct {
	Package string
	Name    string // empty for a package that failed without a failing test
	Passed  bool
	Output  string
}

// Key is the test's name in a baseline: the package and the test name joined by a dot.
func (t TestRun) Key() string {
	if t.Package == "" {
		return t.Name
	}
	return t.Package + "." + t.Name
}

// NewBaseline records which tests passed in a run of the test class at one base commit.
func NewBaseline(treeHash string, runs []TestRun) Baseline {
	b := Baseline{TreeHash: treeHash, Passed: map[string]bool{}}
	for _, r := range runs {
		b.Passed[r.Key()] = r.Passed
	}
	return b
}

// goEvent is one line of go test -json output.
type goEvent struct {
	Action  string
	Package string
	Test    string
	Output  string
}

// ParseGoTestJSON reads go test -json output into one run per test.
// A test whose subtest failed is left out beside the subtest, and a package that failed without a failing test is one run with an empty name.
func ParseGoTestJSON(out string) []TestRun {
	type key struct{ pkg, test string }
	results := map[key]*TestRun{}
	var order []key
	for _, ln := range strings.Split(out, "\n") {
		var e goEvent
		if !strings.HasPrefix(ln, "{") || json.Unmarshal([]byte(ln), &e) != nil || e.Package == "" {
			continue
		}
		k := key{e.Package, e.Test}
		r := results[k]
		if r == nil {
			r = &TestRun{Package: e.Package, Name: e.Test, Passed: true}
			results[k] = r
			order = append(order, k)
		}
		switch e.Action {
		case "output":
			r.Output += e.Output
		case "fail":
			r.Passed = false
		}
	}
	failedTest := map[string]bool{} // packages with a failing test
	hasFailedSub := map[key]bool{}  // tests with a failing subtest
	for _, k := range order {
		if k.test == "" || results[k].Passed {
			continue
		}
		failedTest[k.pkg] = true
		for i := strings.LastIndex(k.test, "/"); i > 0; i = strings.LastIndex(k.test[:i], "/") {
			hasFailedSub[key{k.pkg, k.test[:i]}] = true
		}
	}
	var runs []TestRun
	for _, k := range order {
		r := results[k]
		switch {
		case k.test == "" && (r.Passed || failedTest[k.pkg]):
			continue
		case hasFailedSub[k]:
			continue
		}
		runs = append(runs, *r)
	}
	return runs
}

// goListLine splits a go list line into the package and its dependencies, dropping a "[x.test]" variant tag.
func goListLine(ln string) (pkg string, deps []string) {
	f := strings.Fields(ln)
	if len(f) == 0 {
		return "", nil
	}
	pkg, f = f[0], f[1:]
	if len(f) > 0 && strings.HasPrefix(f[0], "[") {
		for len(f) > 0 && !strings.HasSuffix(f[0], "]") {
			f = f[1:]
		}
		if len(f) > 0 {
			f = f[1:]
		}
	}
	return pkg, f
}

// parseGoList reads `go list -test` output into a package to dependencies map.
// Generated .test mains are skipped and an external test package is merged into its package.
func parseGoList(out string) map[string][]string {
	sets := map[string]map[string]bool{}
	for _, ln := range strings.Split(out, "\n") {
		pkg, deps := goListLine(ln)
		if pkg == "" || strings.HasSuffix(pkg, ".test") {
			continue
		}
		pkg = strings.TrimSuffix(pkg, "_test")
		if sets[pkg] == nil {
			sets[pkg] = map[string]bool{}
		}
		for _, d := range deps {
			if d = strings.TrimSuffix(d, "_test"); d != pkg {
				sets[pkg][d] = true
			}
		}
	}
	res := make(map[string][]string, len(sets))
	for p, s := range sets {
		res[p] = make([]string, 0, len(s))
		for d := range s {
			res[p] = append(res[p], d)
		}
		sort.Strings(res[p])
	}
	return res
}

// shQuote wraps s in single quotes for a shell command line.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// soloGoCommand returns the command that reruns one Go test alone, bypassing the test cache.
func soloGoCommand(t TestRun) string {
	var parts []string
	for _, p := range strings.Split(t.Name, "/") {
		parts = append(parts, "^"+regexp.QuoteMeta(p)+"$")
	}
	return "go test -run " + shQuote(strings.Join(parts, "/")) + " -count=1 " + shQuote(t.Package)
}
