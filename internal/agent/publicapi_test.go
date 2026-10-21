package agent

import (
	"slices"
	"testing"
)

// goDiff wraps hunk lines in a one-file diff of path; head is the hunk header's trailing function text.
func goDiff(path, head string, lines ...string) string {
	d := "diff --git a/" + path + " b/" + path + "\n--- a/" + path + "\n+++ b/" + path + "\n@@ -1,3 +1,3 @@ " + head + "\n"
	for _, l := range lines {
		d += l + "\n"
	}
	return d
}

func TestPublicAPIChanged(t *testing.T) {
	tests := []struct {
		name string
		diff string
		want bool
	}{
		{"exported func added", goDiff("a/a.go", "", "+func Run() error {"), true},
		{"unexported func added", goDiff("a/a.go", "", "+func run() error {"), false},
		{"exported func removed", goDiff("a/a.go", "", "-func Run() error {"), true},
		{"exported method", goDiff("a/a.go", "", "+func (s *Store) Get(k string) string {"), true},
		{"generic receiver method", goDiff("a/a.go", "", "+func (s *Store[K, V]) Get(k K) V {"), true},
		{"exported type", goDiff("a/a.go", "", "+type Store struct {"), true},
		{"exported var and const", goDiff("a/a.go", "", "+var Debug = false", "-const Max = 3"), true},
		{"unexported decls", goDiff("a/a.go", "", "+type store struct {", "+var debug = 1", "+const max = 3"), false},
		{"const in block", goDiff("a/a.go", "const (", " \tfirst = 1", "+\tSecond = 2", " )"), true},
		{"unexported const in block", goDiff("a/a.go", "const (", " \tFirst = 1", "+\tsecond = 2", " )"), false},
		{"field of exported struct", goDiff("a/a.go", "type Store struct {", "+\tName string"), true},
		{"field of unexported struct", goDiff("a/a.go", "type store struct {", "+\tName string"), false},
		{"unexported field of exported struct", goDiff("a/a.go", "type Store struct {", "+\tname string"), false},
		{"embedded exported type", goDiff("a/a.go", "type Store struct {", "+\tio.Reader"), true},
		{"interface method", goDiff("a/a.go", "type Store interface {", "-\tGet(k string) string"), true},
		{"block seen from context lines", goDiff("a/a.go", "", " type Store struct {", "+\tName string", " }"), true},
		{"body after a func line in context",
			goDiff("a/a.go", "", " func run() {", "+\tFoo(1)", " }", " type store struct {", "+\tName string", " }"), false},
		{"wrapped exported param",
			goDiff("a/a.go", "", " func Run(", " \tctx context.Context,", "-\tn int,", "+\tn string,", " ) error {"), true},
		{"body edit under a wrapped header",
			goDiff("a/a.go", "func Run(", " \tx := 1", "-\ty := 2", "+\ty := 3", " \treturn nil"), false},
		{"body edit before a later function's wrapped signature",
			goDiff(
				"a/a.go", "func Run(", "-\ty := 2", "+\ty := 3", " }", " ", " func helper(", " \tn int,", " ) error {",
			), false},
		{"wrapped exported result",
			goDiff("a/a.go", "", " func Run(", " \tn int,", "-) error {", "+) (int, error) {"), true},
		{"header-only wrapped param",
			goDiff("a/a.go", "func Run(", " \tctx context.Context,", "-\tn int,", "+\tn string,", " ) error {"), true},
		{"header-only wrapped result",
			goDiff("a/a.go", "func Run(", " \tctx context.Context,", " \tn int,", "-) error {", "+) (int, error) {"), true},
		{"header-only unexported wrapped param",
			goDiff("a/a.go", "func run(", "-\tn int,", "+\tn string,", " ) error {"), false},
		{"wrapped unexported param", goDiff("a/a.go", "func run(", "-\tn int,", "+\tn string,", " ) error {"), false},
		{"wrapped signature from context line",
			goDiff("a/a.go", "", " func Run(", "-\tn int,", "+\tn string,", " ) error {"), true},
		{"body change only", goDiff("a/a.go", "func Run() error {", "+\treturn nil", "-\tx := 1"), false},
		{"unknown context fails closed", goDiff("a/a.go", "", "+\tName string"), true},
		{"new file with only a package line", goDiff("a/a.go", "", "+package a"), false},
		{"test file only", goDiff("a/a_test.go", "", "+func TestRun(t *testing.T) {", "+type Fake struct{}"), false},
		{"non-go file", goDiff("docs/a.md", "", "+func Run() error {"), false},
		{"renamed func", goDiff("a/a.go", "", "-func Run() error {", "+func Start() error {"), true},
		{"test file beside public change", goDiff("a/a_test.go", "", "+func TestX() {") +
			goDiff("a/a.go", "", "+func Run() {"), true},
		{"renamed go file", "diff --git a/a/a.go b/b/a.go\nrename from a/a.go\nrename to b/a.go\n", true},
		{"binary go file", "diff --git a/a.go b/a.go\nBinary files a/a.go and b/a.go differ\n", true},
		{"empty diff", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := PublicAPIChanged(tc.diff); got != tc.want {
				t.Errorf("PublicAPIChanged = %v, want %v", got, tc.want)
			}
		})
	}
}

// A public change raises a Go diff to med_high, so the Reviewer runs and the deep plan holds fuzz.
func TestClassifyPublicAPIFromDiff(t *testing.T) {
	diff := goDiff("internal/a/a.go", "", "+func Run() error {")
	d := Classify(RiskInput{Files: DiffFiles(diff), PublicAPI: PublicAPIChanged(diff)})

	if d.Level != RiskMedHigh {
		t.Fatalf("level = %v, want med_high (%s)", d.Level, d.Reason)
	}
	r := Route(d.Level)
	if !r.Reviewer || !slices.Contains(r.Deep, DeepFuzz) {
		t.Errorf("route = %+v, want the Reviewer and fuzz", r)
	}
}
