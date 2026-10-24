package intake

import (
	"reflect"
	"strings"
	"testing"
)

func TestBuildGraph(t *testing.T) {
	t.Parallel()

	t.Run("waves and the longest chain", func(t *testing.T) {
		t.Parallel()

		b := bpPlan(
			bpGroup("tg-1"),
			bpGroup("tg-2"),
			bpGroup("tg-3", "tg-1", "tg-2"),
			bpGroup("tg-4", "tg-3"),
		)

		g, fs := BuildGraph(b, nil)
		if len(fs) != 0 {
			t.Fatalf("findings: %s", findingText(fs))
		}

		want := [][]string{{"tg-1", "tg-2"}, {"tg-3"}, {"tg-4"}}
		if got := g.Waves(); !reflect.DeepEqual(got, want) {
			t.Fatalf("waves: %v", got)
		}

		if n := g.LongestChain(); n != 3 {
			t.Fatalf("chain: %d", n)
		}
	})

	t.Run("overlapping touches need an order", func(t *testing.T) {
		t.Parallel()

		a, b := bpGroup("tg-1"), bpGroup("tg-2")
		a.Touches, a.Files = []string{"internal/lease/**"}, []string{"internal/lease/a.go"}
		b.Touches, b.Files = []string{"internal/lease/*.go"}, []string{"internal/lease/b.go"}

		_, fs := BuildGraph(bpPlan(a, b), []string{"internal/lease/fence.go"})

		got := only(t, fs, RuleGraph, SeverityFail, 1)
		if !strings.Contains(got[0].Message, "internal/lease/a.go") ||
			!strings.Contains(got[0].Message, "tg-1") || !strings.Contains(got[0].Message, "tg-2") ||
			got[0].Path != "plan.md#tg-2" {
			t.Fatalf("finding: %+v", got[0])
		}

		b.Dependencies = []string{"tg-1"}
		if _, fs := BuildGraph(bpPlan(a, b), nil); len(fails(fs)) != 0 {
			t.Fatalf("ordered pair: %s", findingText(fs))
		}
	})

	t.Run("an order through a third group counts", func(t *testing.T) {
		t.Parallel()

		a, mid, c := bpGroup("tg-1"), bpGroup("tg-2", "tg-1"), bpGroup("tg-3", "tg-2")
		a.Touches, a.Files = []string{"x/**"}, []string{"x/a.go"}
		c.Touches, c.Files = []string{"x/**"}, []string{"x/c.go"}

		if _, fs := BuildGraph(bpPlan(a, mid, c), nil); len(fails(fs)) != 0 {
			t.Fatalf("findings: %s", findingText(fs))
		}
	})

	t.Run("a cycle is a failing graph finding", func(t *testing.T) {
		t.Parallel()

		_, fs := BuildGraph(bpPlan(bpGroup("tg-1", "tg-2"), bpGroup("tg-2", "tg-1")), nil)

		got := only(t, fs, RuleGraph, SeverityFail, 1)
		if !strings.Contains(got[0].Message, "tg-1 -> tg-2 -> tg-1") {
			t.Fatalf("finding: %+v", got[0])
		}
	})

	t.Run("a group every other group waits on warns", func(t *testing.T) {
		t.Parallel()

		_, fs := BuildGraph(bpPlan(bpGroup("tg-1"), bpGroup("tg-2", "tg-1"), bpGroup("tg-3", "tg-1")), nil)

		got := only(t, fs, RuleGraph, SeverityWarn, 1)
		if got[0].Path != "plan.md#tg-1" {
			t.Fatalf("finding: %+v", got[0])
		}
	})
}
