package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	goTotal     = regexp.MustCompile(`(?m)^total:\s+\(statements\)\s+([0-9.]+)%`)
	jacocoLine  = regexp.MustCompile(`<counter type="LINE" missed="(\d+)" covered="(\d+)"\s*/>`)
	lineStartJS = regexp.MustCompile(`(?m)^\{`)
)

// CoverageCommand returns the default command that prints a whole-repository line coverage report.
// JavaScript and TypeScript wrap the repository's own test command, so they need it.
// Java and Kotlin have no default: their command comes from the configuration.
func CoverageCommand(language, testCmd string) (string, bool) {
	switch language {
	case "go":
		return "go test -coverpkg=./... -coverprofile=c.out ./...; go tool cover -func=c.out", true
	case "python":
		return "coverage run -m pytest; coverage json -o -", true
	case "javascript", "typescript":
		if testCmd == "" {
			return "", false
		}
		return "c8 --reporter=json-summary " + testCmd + "; cat coverage/coverage-summary.json", true
	case "rust":
		return "cargo llvm-cov --json --summary-only", true
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
