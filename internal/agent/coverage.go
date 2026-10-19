package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

var (
	goTotal     = regexp.MustCompile(`(?m)^total:\s+\(statements\)\s+([0-9.]+)%`)
	jacocoLine  = regexp.MustCompile(`<counter type="LINE" missed="(\d+)" covered="(\d+)"\s*/>`)
	lineStartJS = regexp.MustCompile(`(?m)^\{`)
	goBlock     = regexp.MustCompile(`(?m)^(\S+\.go):(\d+)\.\d+,(\d+)\.\d+ \d+ (\d+)\s*$`)
	jacocoNode  = regexp.MustCompile(
		`<package name="([^"]*)"|<sourcefile name="([^"]*)"|<line nr="(\d+)" mi="\d+" ci="(\d+)"`,
	)
)

// CoverageCommand returns the default command that prints a whole-repository line coverage report.
// The Go command also prints the raw profile, which holds the line data.
// JavaScript and TypeScript wrap the repository's own test command, so they need it.
// Java and Kotlin have no default: their command comes from the configuration.
func CoverageCommand(language, testCmd string) (string, bool) {
	switch language {
	case "go":
		return "go test -coverpkg=./... -coverprofile=c.out ./...; go tool cover -func=c.out; cat c.out", true
	case "python":
		return "coverage run -m pytest; coverage json -o -", true
	case "javascript", "typescript":
		if testCmd == "" {
			return "", false
		}
		return "c8 --reporter=json-summary " + testCmd + "; cat coverage/coverage-summary.json", true
	case "rust":
		return "cargo llvm-cov --ignore-run-fail --json --summary-only", true
	}
	return "", false
}

// pyReport, jsReport and rustReport hold the coverage field of each tool's JSON report.
type pyReport struct {
	Totals struct {
		P *float64 `json:"percent_covered"`
	} `json:"totals"`
}

type jsReport struct {
	Total struct {
		Lines struct {
			Pct *float64 `json:"pct"`
		} `json:"lines"`
	} `json:"total"`
}

type rustReport struct {
	Data []struct {
		Totals struct {
			Lines struct {
				Percent *float64 `json:"percent"`
			} `json:"lines"`
		} `json:"totals"`
	} `json:"data"`
}

// ParseCoverage reads the whole-repository line percent from a coverage command's output.
func ParseCoverage(language, output string) (float64, error) {
	switch language {
	case "go":
		if m := goTotal.FindStringSubmatch(output); m != nil {
			return strconv.ParseFloat(m[1], 64)
		}
	case "python":
		if v, ok := lastJSON(output, func(r pyReport) bool { return r.Totals.P != nil }); ok {
			return *v.Totals.P, nil
		}
	case "javascript", "typescript":
		if v, ok := lastJSON(output, func(r jsReport) bool { return r.Total.Lines.Pct != nil }); ok {
			return *v.Total.Lines.Pct, nil
		}
	case "rust":
		if v, ok := lastJSON(
			output,
			func(r rustReport) bool { return len(r.Data) > 0 && r.Data[0].Totals.Lines.Percent != nil },
		); ok {
			return *v.Data[0].Totals.Lines.Percent, nil
		}
	case "java", "kotlin":
		if v, ok := jacocoPercent(output); ok {
			return v, nil
		}
	default:
		return 0, fmt.Errorf("no coverage tool for language %s", language)
	}
	return 0, fmt.Errorf("no %s coverage percent in the output", language)
}

// lastJSON returns the last JSON object that starts a line of out and passes has.
func lastJSON[T any](out string, has func(T) bool) (T, bool) {
	locs := lineStartJS.FindAllStringIndex(out, -1)
	for i := len(locs) - 1; i >= 0; i-- {
		var v T
		if json.NewDecoder(strings.NewReader(out[locs[i][0]:])).Decode(&v) == nil && has(v) {
			return v, true
		}
	}
	var zero T
	return zero, false
}

// CoverageLevel judges head coverage against the floor; a base already under the floor only forbids lowering it.
func CoverageLevel(floor int, base float64, haveBase bool, head float64) Level {
	if head >= float64(floor) {
		return LevelPass
	}
	if haveBase && base < float64(floor) && head >= base {
		return LevelPass
	}
	return LevelFail
}

// jacocoPercent adds up the report-level LINE counters of every JaCoCo report in the output, one report per module.
// The report-level counter is the last LINE counter of its report; package and class counters come first.
func jacocoPercent(out string) (float64, bool) {
	var missed, covered int
	for _, doc := range strings.Split(out, "<report") {
		all := jacocoLine.FindAllStringSubmatch(doc, -1)
		if len(all) == 0 {
			continue
		}

		m := all[len(all)-1]
		a, _ := strconv.Atoi(m[1])
		b, _ := strconv.Atoi(m[2])
		missed, covered = missed+a, covered+b
	}

	if missed+covered == 0 {
		return 0, false
	}
	return float64(covered) * 100 / float64(missed+covered), true
}

// LineCoverage maps a file to its executable lines; a line is true when a test ran it.
type LineCoverage map[string]map[int]bool

// ChangedLines is the coverage of the lines a change adds.
type ChangedLines struct {
	Uncovered  []string // file:line or file:first-last of added executable lines no test ran
	Executable int      // added lines the coverage tool counts
	Covered    int      // added executable lines a test ran
}

// ParseLineCoverage reads per-line coverage from a coverage command's output. It reports false when the language's output
// carries no line data: JavaScript, TypeScript and Rust summaries hold totals only.
func ParseLineCoverage(language, output string) (LineCoverage, bool) {
	var lc LineCoverage
	switch language {
	case "go":
		lc = goLines(output)
	case "python":
		lc = pythonLines(output)
	case "java", "kotlin":
		lc = jacocoLines(output)
	}
	return lc, len(lc) > 0
}

// ChangedCoverage intersects the added lines of a unified diff with the line coverage. A line the tool does not count,
// such as a comment or a declaration, is left out.
func ChangedCoverage(diff string, lc LineCoverage) ChangedLines {
	var res ChangedLines
	keys := map[string]string{}
	missed := map[string][]int{}

	for _, d := range diffLines(diff) {
		if !d.Added || d.Line < 1 || d.File == "" {
			continue
		}

		key, seen := keys[d.File]
		if !seen {
			key = lc.find(d.File)
			keys[d.File] = key
		}

		covered, executable := lc[key][d.Line]
		switch {
		case !executable:
		case covered:
			res.Executable++
			res.Covered++
		default:
			res.Executable++
			missed[d.File] = append(missed[d.File], d.Line)
		}
	}

	files := make([]string, 0, len(missed))
	for f := range missed {
		files = append(files, f)
	}
	sort.Strings(files)

	for _, f := range files {
		res.Uncovered = append(res.Uncovered, lineRanges(f, missed[f])...)
	}
	return res
}

// find returns the coverage key of a repository path: the same path, or one that ends the other after a slash.
// The Go profile names files by import path and JaCoCo by package, so they are shorter or longer than the diff path.
func (lc LineCoverage) find(file string) string {
	if _, ok := lc[file]; ok {
		return file
	}

	keys := make([]string, 0, len(lc))
	for k := range lc {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		if strings.HasSuffix(k, "/"+file) || strings.HasSuffix(file, "/"+k) {
			return k
		}
	}
	return ""
}

// lineRanges formats sorted-or-not line numbers of one file as file:line and file:first-last entries.
func lineRanges(file string, lines []int) []string {
	slices.Sort(lines)

	var out []string
	for i := 0; i < len(lines); {
		j := i
		for j+1 < len(lines) && lines[j+1] == lines[j]+1 {
			j++
		}

		if i == j {
			out = append(out, fmt.Sprintf("%s:%d", file, lines[i]))
		} else {
			out = append(out, fmt.Sprintf("%s:%d-%d", file, lines[i], lines[j]))
		}
		i = j + 1
	}
	return out
}

// goLines reads the profile blocks of a Go coverage run; a line is covered when any block that holds it ran.
func goLines(out string) LineCoverage {
	lc := LineCoverage{}
	for _, m := range goBlock.FindAllStringSubmatch(out, -1) {
		first, _ := strconv.Atoi(m[2])
		last, _ := strconv.Atoi(m[3])
		count, _ := strconv.Atoi(m[4])

		if lc[m[1]] == nil {
			lc[m[1]] = map[int]bool{}
		}
		for n := first; n <= last; n++ {
			lc[m[1]][n] = lc[m[1]][n] || count > 0
		}
	}
	return lc
}

// pyFiles holds the per-file line lists of a coverage json report.
type pyFiles struct {
	Files map[string]struct {
		Executed []int `json:"executed_lines"`
		Missing  []int `json:"missing_lines"`
	} `json:"files"`
}

// pythonLines reads the executed and missing lines of each file in a coverage json report.
func pythonLines(out string) LineCoverage {
	rep, ok := lastJSON(out, func(r pyFiles) bool { return len(r.Files) > 0 })
	if !ok {
		return nil
	}

	lc := LineCoverage{}
	for f, d := range rep.Files {
		lc[f] = map[int]bool{}
		for _, n := range d.Missing {
			lc[f][n] = false
		}
		for _, n := range d.Executed {
			lc[f][n] = true
		}
	}
	return lc
}

// jacocoLines reads the line counters of each source file in JaCoCo XML, keyed by package folder and file name.
func jacocoLines(out string) LineCoverage {
	lc := LineCoverage{}
	pkg, file := "", ""

	for _, m := range jacocoNode.FindAllStringSubmatch(out, -1) {
		switch {
		case strings.HasPrefix(m[0], "<package"):
			pkg = m[1]
		case strings.HasPrefix(m[0], "<sourcefile"):
			file = strings.TrimPrefix(pkg+"/"+m[2], "/")
			lc[file] = map[int]bool{}
		case file != "":
			n, _ := strconv.Atoi(m[3])
			hit, _ := strconv.Atoi(m[4])
			lc[file][n] = hit > 0
		}
	}
	return lc
}
