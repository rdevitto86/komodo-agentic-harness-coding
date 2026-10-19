//go:build !windows

package integration

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureDir is the checked-in repository the end-to-end checks copy.
const fixtureDir = "testdata/fixture-repo"

// groupID is the identifier of the fixture's task group tg-1.1.
const groupID = "0192e4c5-7a10-7d3e-9b2f-000000000301"

// fixtureFiles reads the fixture and splits it into the repository files and the backlog files the planner returns.
func fixtureFiles(t *testing.T) (repoFiles, backlog map[string]string) {
	t.Helper()
	repoFiles, backlog = map[string]string{}, map[string]string{}
	err := filepath.WalkDir(fixtureDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(fixtureDir, p)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "docs/backlog/") {
			backlog[rel] = string(b)
		} else {
			repoFiles[rel] = string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return repoFiles, backlog
}

// merged returns the union of the given file sets, later sets winning.
func merged(sets ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, s := range sets {
		for k, v := range s {
			out[k] = v
		}
	}
	return out
}

// newFixtureRig builds a rig over a fresh copy of the fixture. With committed set the backlog is already in the
// repository; otherwise only the fake planner knows it. extra files override the fixture's.
func newFixtureRig(t *testing.T, committed bool, extra map[string]string) *binRig {
	t.Helper()
	repoFiles, backlog := fixtureFiles(t)
	files := merged(repoFiles, extra)
	if committed {
		files = merged(files, backlog)
	}
	r := newBinRig(t, files)
	r.model.plan = backlog
	r.model.builder = fixtureBuilder(false)
	return r
}

const (
	subSource = "package calc\n\n// Sub returns a minus b.\nfunc Sub(a, b int) int { return a - b }\n"
	subTest   = "package calc\n\nimport \"testing\"\n\nfunc TestSub(t *testing.T) {\n" +
		"\tfor _, c := range []struct{ a, b, want int }{{3, 2, 1}, {0, 0, 0}, {1, 3, -2}} {\n" +
		"\t\tif got := Sub(c.a, c.b); got != c.want {\n\t\t\tt.Fatalf(\"Sub(%d, %d) = %d, want %d\", c.a, c.b, got, c.want)\n" +
		"\t\t}\n\t}\n}\n"
	mulSource = "package calc\n\n// Mul returns a times b.\nfunc Mul(a, b int) int { return a * b }\n"
	mulTest   = "package calc\n\nimport \"testing\"\n\nfunc TestMul(t *testing.T) {\n" +
		"\tfor _, c := range []struct{ a, b, want int }{{3, 2, 6}, {0, 5, 0}, {-1, 3, -3}} {\n" +
		"\t\tif got := Mul(c.a, c.b); got != c.want {\n\t\t\tt.Fatalf(\"Mul(%d, %d) = %d, want %d\", c.a, c.b, got, c.want)\n" +
		"\t\t}\n\t}\n}\n"
	// breakAdd is fixture B's builder change: Add subtracts, so TestAdd breaks.
	breakAdd = "package calc\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int { return a - b }\n"
)

// fixtureBuilder scripts the fake builder for tg-fixture: Sub, then Mul, each with its table test and a task_done. With
// broken set, the builder first rewrites Add so that it subtracts (fixture B).
func fixtureBuilder(broken bool) func(brief string, turn int) (string, map[string]any) {
	return fixtureBuilderGated(broken, nil)
}

// fixtureBuilderGated is fixtureBuilder with a gate that runs before the builder's first step of every task after the first;
// it may block. Tasks other than sub and mul get a source file named after the task.
func fixtureBuilderGated(broken bool, gate func(task string)) func(brief string, turn int) (string, map[string]any) {
	return func(brief string, turn int) (string, map[string]any) {
		write := func(p, c string) [2]any { return [2]any{"write", map[string]any{"path": p, "content": c}} }
		var steps [][2]any
		var starts []int
		var ids []string
		for _, m := range taskLine.FindAllStringSubmatch(brief, -1) {
			ids = append(ids, m[1])
		}
		if broken {
			steps = append(steps, write("calc/calc.go", breakAdd))
		}
		for _, id := range ids {
			starts = append(starts, len(steps))
			name := strings.TrimPrefix(id, "tsk-")
			switch name {
			case "sub":
				steps = append(steps, write("calc/sub.go", subSource), write("calc/sub_test.go", subTest))
			case "mul":
				steps = append(steps, write("calc/mul.go", mulSource), write("calc/mul_test.go", mulTest))
			default:
				steps = append(steps, write("calc/"+name+".go", "package calc\n\n// F"+name+" is a fixture function.\n"+
					"func F"+name+"() int { return 1 }\n"))
			}
			steps = append(steps, [2]any{"task_done", map[string]any{"task": id}})
		}
		for i, s := range starts {
			if s == turn && i > 0 && gate != nil {
				gate(ids[i])
			}
		}
		if turn >= len(steps) {
			return "", nil
		}
		return steps[turn][0].(string), steps[turn][1].(map[string]any)
	}
}

// taskByName returns the task id holding name, or the first id.
func taskByName(ids []string, name string) string {
	for _, id := range ids {
		if strings.Contains(id, name) {
			return id
		}
	}
	return ids[0]
}
