//go:build !windows

package integration

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	// breakAddOld and breakAddNew are fixture B's first builder edit: Add subtracts, so TestAdd breaks.
	breakAddOld = "return a + b"
	breakAddNew = "return a - b"
)

// fixtureBuilder scripts the fake builder for tg-fixture: Sub, then Mul, each with its table test and a task_done. With
// broken set, the builder first edits Add so that it subtracts (fixture B); in each later session it breaks Add
// further, so the tree and the failure both differ and the return is not a flap.
func fixtureBuilder(broken bool) func(brief string, turn int) (string, map[string]any) {
	return fixtureBuilderGated(broken, nil)
}

// fixtureBuilderGated is fixtureBuilder with a gate that runs before the builder's first step of every task after the first;
// it may block. Tasks other than sub and mul get a source file named after the task.
func fixtureBuilderGated(broken bool, gate func(task string)) func(brief string, turn int) (string, map[string]any) {
	// A return resumes the builder's session, so its turn count continues; rounds counts the turns that ended with no
	// further tool call, and first is the turn at which the current round began.
	var mu sync.Mutex
	rounds, first, begun := 0, 0, false
	return func(brief string, turn int) (string, map[string]any) {
		mu.Lock()
		if !begun {
			first, begun = turn, true
		}
		n, turn := rounds+1, turn-first
		mu.Unlock()
		endRound := func() (string, map[string]any) {
			mu.Lock()
			rounds, begun = rounds+1, false
			mu.Unlock()
			return "", nil
		}
		write := func(p, c string) [2]any { return [2]any{"write", map[string]any{"path": p, "content": c}} }
		var steps [][2]any
		var starts []int
		var ids []string
		for _, m := range taskLine.FindAllStringSubmatch(brief, -1) {
			ids = append(ids, m[1])
		}
		if broken {
			old, repl := breakAddOld, breakAddNew
			if n > 1 {
				// Each return breaks Add further, so the failure differs from the last one and is not a flap.
				old, repl = breakAddNew, breakAddNew+" - "+strconv.Itoa(n-1)
				if n > 2 {
					old = breakAddNew + " - " + strconv.Itoa(n-2)
				}
			}
			steps = append(steps, [2]any{"edit", map[string]any{"path": "calc/calc.go", "old": old, "new": repl}})
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
					"func F"+name+"() int { return 1 }\n"),
					write("calc/"+name+"_test.go", "package calc\n\nimport \"testing\"\n\n"+
						"func TestF"+name+"(t *testing.T) {\n\tif F"+name+"() != 1 {\n\t\tt.Fatal(\"F"+name+" is not 1\")\n\t}\n}\n"))
			}
			steps = append(steps, [2]any{"task_done", map[string]any{"task": id}})
		}
		for i, s := range starts {
			if s == turn && i > 0 && gate != nil {
				gate(ids[i])
			}
		}
		if n > 1 && broken {
			steps = steps[:1]
		}
		if turn >= len(steps) {
			return endRound()
		}
		return steps[turn][0].(string), steps[turn][1].(map[string]any)
	}
}
