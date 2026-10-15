package fault

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/format"
)

// codeExits reads the code-to-exit table of the design document.
func codeExits(t *testing.T) map[string]int {
	t.Helper()
	b, err := os.ReadFile("../../docs/lld.md")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(b), "#### Error codes the Error Handler prints")
	if !ok {
		t.Fatal("code table heading not found")
	}
	section, _, _ := strings.Cut(rest, "A held group's block reason")
	row := regexp.MustCompile("^\\| ((?:`[a-z_]+`(?:, )?)+) \\| (\\d) \\|")
	code := regexp.MustCompile("`([a-z_]+)`")
	out := map[string]int{}
	for _, line := range strings.Split(section, "\n") {
		m := row.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[2])
		for _, c := range code.FindAllStringSubmatch(m[1], -1) {
			out[c[1]] = n
		}
	}
	return out
}

// TestCodeTableMatchesExitRule checks the documented exit of each code the package emits equals the exit its fault selects.
func TestCodeTableMatchesExitRule(t *testing.T) {
	table := codeExits(t)
	if len(table) < 20 {
		t.Fatalf("parsed %d rows, want the whole table", len(table))
	}
	faults := map[string]Fault{
		string(format.Internal):    {Kind: KindInternal, Severity: SeverityError},
		string(format.BusOverflow): {Kind: KindInternal, Severity: SeverityWarn},
		string(format.OverLimit):   {Kind: KindRefused, Severity: SeverityError},
	}
	for _, row := range otherErrors {
		faults[string(row.code)] = Fault{Kind: row.kind, Severity: row.severity}
	}
	for c, f := range faults {
		want, ok := table[c]
		if !ok {
			t.Errorf("%s is not in the code table", c)
			continue
		}
		if got := f.ExitCode(); got != want {
			t.Errorf("%s: table says %d, fault selects %d", c, want, got)
		}
	}
	pinned := map[string]int{"plan_blocked": 3, "plan_timeout": 1, "plan_input": 2, "plan_write": 4, "bus_overflow": 0, "internal": 4}
	for c, want := range pinned {
		if table[c] != want {
			t.Errorf("%s: table says %d, want %d", c, table[c], want)
		}
	}
}
