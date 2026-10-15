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
// JavaScript, TypeScript, Java and Kotlin wrap the repository's own test command, so they need it.
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
	case "java", "kotlin":
		if testCmd == "" {
			return "", false
		}
		return testCmd + "; cat build/reports/jacoco/test/jacocoTestReport.xml", true
	}
	return "", false
}

// ParseCoverage reads the whole-repository line percent from a coverage command's output.
func ParseCoverage(language, output string) (float64, error) {
	switch language {
	case "go":
		if m := goTotal.FindStringSubmatch(output); m != nil {
			return strconv.ParseFloat(m[1], 64)
		}
	case "python":
		var v struct {
			Totals struct {
				P *float64 `json:"percent_covered"`
			} `json:"totals"`
		}
		if lastJSON(output, &v) && v.Totals.P != nil {
			return *v.Totals.P, nil
		}
	case "javascript", "typescript":
		var v struct {
			Total struct {
				Lines struct {
					Pct *float64 `json:"pct"`
				} `json:"lines"`
			} `json:"total"`
		}
		if lastJSON(output, &v) && v.Total.Lines.Pct != nil {
			return *v.Total.Lines.Pct, nil
		}
	case "rust":
		var v struct {
			Data []struct {
				Totals struct {
					Lines struct {
						Percent *float64 `json:"percent"`
					} `json:"lines"`
				} `json:"totals"`
			} `json:"data"`
		}
		if lastJSON(output, &v) && len(v.Data) > 0 && v.Data[0].Totals.Lines.Percent != nil {
			return *v.Data[0].Totals.Lines.Percent, nil
		}
	case "java", "kotlin":
		if all := jacocoLine.FindAllStringSubmatch(output, -1); len(all) > 0 {
			m := all[len(all)-1]
			missed, _ := strconv.Atoi(m[1])
			covered, _ := strconv.Atoi(m[2])
			if missed+covered > 0 {
				return float64(covered) * 100 / float64(missed+covered), nil
			}
		}
	default:
		return 0, fmt.Errorf("no coverage tool for language %s", language)
	}
	return 0, fmt.Errorf("no %s coverage percent in the output", language)
}

// lastJSON decodes the last JSON object that starts a line of out into v, reporting whether one decoded.
func lastJSON(out string, v any) bool {
	locs := lineStartJS.FindAllStringIndex(out, -1)
	for i := len(locs) - 1; i >= 0; i-- {
		if json.NewDecoder(strings.NewReader(out[locs[i][0]:])).Decode(v) == nil {
			return true
		}
	}
	return false
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
