package intake

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// readPlanFixture returns the sample plan.md.
func readPlanFixture(t *testing.T) string {
	t.Helper()

	b, err := os.ReadFile("testdata/plan.md")
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}

// findingText joins the paths and messages of a finding list.
func findingText(f []Finding) string {
	var m []string
	for _, x := range f {
		m = append(m, x.Path+": "+x.Message)
	}

	return strings.Join(m, "\n")
}

func TestParsePlanFile(t *testing.T) {
	t.Parallel()

	t.Run("reads the sample plan", func(t *testing.T) {
		t.Parallel()

		b, found := Parse(readPlanFixture(t))
		if len(found) != 0 {
			t.Fatalf("findings: %s", findingText(found))
		}

		if b.Plan != "plan-7f3a9c" || b.Goal != "Add lease revocation" || b.Release != "V1" {
			t.Fatalf("header = %+v", b)
		}

		if !reflect.DeepEqual(b.Sources, []string{"docs/hld.md", "docs/lld.md"}) {
			t.Fatalf("sources = %v", b.Sources)
		}

		if len(b.Groups) != 2 {
			t.Fatalf("groups = %d", len(b.Groups))
		}

		g := b.Groups[0]
		if g.Name != "tg-1" || g.ID != "0192e4c5-7a10-7d3e-9b2f-6c1d8e4a5f30" || g.Points != 3 ||
			g.Priority != PriorityHigh || g.Subsystem != "Runtime Manager" ||
			!reflect.DeepEqual(g.Touches, []string{"internal/engine/**"}) ||
			len(g.Files) != 2 || len(g.Dependencies) != 0 || len(g.Tasks) != 2 {
			t.Fatalf("group = %+v", g)
		}

		tk := g.Tasks[0]
		if tk.Name != "tsk-fence-stale" || tk.DoneWhen != "go test ./internal/lease/..." || tk.EstimateLines != 120 {
			t.Fatalf("task = %+v", tk)
		}

		if !strings.Contains(tk.Card, "### not a heading") || !strings.Contains(tk.Card, "- a card bullet") {
			t.Fatalf("card = %q", tk.Card)
		}

		if len(tk.Acceptance) != 2 || tk.Acceptance[0].Keyword != "WHEN" ||
			tk.Acceptance[0].Cmd != "go test ./internal/engine/..." ||
			!reflect.DeepEqual(tk.Acceptance[0].Needs, []string{"network", "docker"}) ||
			tk.Acceptance[1].Keyword != "IF" {
			t.Fatalf("acceptance = %+v", tk.Acceptance)
		}

		g2 := b.Groups[1]
		if g2.Subsystem != "none" || g2.Priority != PriorityNormal || g2.ID != "0192e4c5-7a10-7d3e-9b2f-6c1d8e4a5f31" ||
			!reflect.DeepEqual(g2.Dependencies, []string{"tg-1"}) {
			t.Fatalf("group 2 = %+v", g2)
		}

		if g2.Tasks[0].DoneWhen != "" || len(g2.Tasks[0].Acceptance) != 1 {
			t.Fatalf("task 2 = %+v", g2.Tasks[0])
		}
	})

	t.Run("round trips through Render", func(t *testing.T) {
		t.Parallel()

		b, _ := Parse(readPlanFixture(t))

		again, found := Parse(Render(b))
		if len(found) != 0 {
			t.Fatalf("findings: %s", findingText(found))
		}

		if !reflect.DeepEqual(b, again) {
			t.Fatalf("round trip differs:\n%+v\n%+v", b, again)
		}
	})

	t.Run("reports grammar and required problems", func(t *testing.T) {
		t.Parallel()

		const (
			hdr = "# Plan\ngoal: g\n\n"
			tsk = hdr + "## tg-1\n\n### tsk-a\n"
		)

		cases := []struct{ name, text, want string }{
			{"text before the plan heading", "stray\n# Plan\ngoal: g\n", "before the # Plan heading"},
			{"no plan heading", "## tg-1\n", "# Plan"},
			{"second level one heading", hdr + "# Plan\n", "second # heading"},
			{"header goal missing", "# Plan\nid: plan-aaaaaa\n", "goal is missing"},
			{"header unknown key", "# Plan\ngoal: g\nbogus: 1\n", "unknown key bogus"},
			{"header stray text", hdr + "some prose\n", "text after the field block"},
			{"group heading not tg-N", hdr + "## other\n", "is not tg-N"},
			{"group leading zero", hdr + "## tg-01\n", "is not tg-N"},
			{"group twice", hdr + "## tg-1\n\n## tg-1\n", "appears twice"},
			{"task outside group", hdr + "### tsk-a\n", "tsk-a is outside a group"},
			{"task bad name", hdr + "## tg-1\n\n### other\n", "does not start with tsk-"},
			{"task twice", tsk + "\n### tsk-a\n", "tsk-a appears twice"},
			{"group status is not a field", hdr + "## tg-1\nstatus: done\n", "unknown key status"},
			{"group id not v7", hdr + "## tg-1\nid: nope\n", "not a version 7"},
			{"group goal missing", hdr + "## tg-1\n", "goal is missing"},
			{"group points missing", hdr + "## tg-1\n", "points is missing"},
			{"group points not a number", hdr + "## tg-1\npoints: x\n", "points \"x\" is not a number"},
			{"group priority bad", hdr + "## tg-1\npriority: urgent\n", "priority \"urgent\""},
			{"group files empty", hdr + "## tg-1\n", "files is missing or empty"},
			{"group dependencies missing", hdr + "## tg-1\n", "dependencies is missing"},
			{"group stray text", hdr + "## tg-1\ngoal: x\n\nprose\n", "text after the field block"},
			{"task files missing", tsk + "done_when: x\nestimate_lines: 1\n", "files is missing"},
			{"task no done_when or acceptance", tsk + "files: a\nestimate_lines: 1\n", "neither"},
			{"task estimate missing", tsk + "files: a\ndone_when: x\n", "estimate_lines is missing"},
			{"task estimate not a number", tsk + "estimate_lines: z\n", "estimate_lines \"z\""},
			{"acceptance bad needs", tsk + "files: a\n\n- WHEN x THE SYSTEM SHALL y\n  needs: moon\n", "needs value"},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				t.Parallel()

				_, found := Parse(c.text)
				if got := findingText(found); !strings.Contains(got, c.want) {
					t.Fatalf("want %q in:\n%s", c.want, got)
				}
			})
		}
	})

	t.Run("finding paths name the section", func(t *testing.T) {
		t.Parallel()

		_, found := Parse("# Plan\ngoal: g\n\n## tg-1\n\n### tsk-a\nfiles: a\n")
		if got := findingText(found); !strings.Contains(got, "plan.md#tg-1/tsk-a: ") ||
			!strings.Contains(got, "plan.md#tg-1: ") {
			t.Fatalf("paths: %s", got)
		}
	})
}

func TestRenderPlanFile(t *testing.T) {
	t.Parallel()

	t.Run("omits what is empty and keeps required keys", func(t *testing.T) {
		t.Parallel()

		out := Render(Blueprint{
			Goal: "g",
			Groups: []PlanGroup{{
				Name: "tg-1", Goal: "x", Points: 1, Files: []string{"a"},
				Tasks: []PlanTask{{Name: "tsk-a", Files: []string{"a"}, DoneWhen: "true", EstimateLines: 5}},
			}},
		})
		for _, bad := range []string{"id:", "release:", "sources:", "subsystem:", "touches:", "priority:"} {
			if strings.Contains(out, bad) {
				t.Fatalf("%q written for an empty value:\n%s", bad, out)
			}
		}

		if !strings.Contains(out, "dependencies:\n") {
			t.Fatalf("dependencies key missing:\n%s", out)
		}
	})
}
