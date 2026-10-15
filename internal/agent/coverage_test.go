package agent

import "testing"

func TestParseCoverage(t *testing.T) {
	cases := []struct {
		lang, out string
		want      float64
	}{
		{"go", "pkg/a.go:3:\tF\t100.0%\ntotal:\t\t\t\t(statements)\t83.3%\n", 83.3},
		{"python", "===== 3 passed =====\n{\"meta\": {}, \"totals\": {\"percent_covered\": 91.25}}\n", 91.25},
		{"javascript", "tests ok\n{\"total\":{\"lines\":{\"total\":10,\"covered\":9,\"pct\":90}}}\n", 90},
		{"typescript", "{\"total\":{\"lines\":{\"pct\":77.5}}}", 77.5},
		{"rust", "{\"data\":[{\"totals\":{\"lines\":{\"percent\":64.5}}}],\"type\":\"llvm.coverage.json.export\"}\n", 64.5},
		{"java", "<report><counter type=\"LINE\" missed=\"5\" covered=\"15\"/><package><counter type=\"LINE\" missed=\"1\" covered=\"1\"/></package><counter type=\"LINE\" missed=\"10\" covered=\"90\"/></report>", 90},
		{"kotlin", "<counter type=\"LINE\" missed=\"0\" covered=\"4\"/>", 100},
	}
	for _, c := range cases {
		got, err := ParseCoverage(c.lang, c.out)
		if err != nil || got != c.want {
			t.Errorf("%s: got %v, %v want %v", c.lang, got, err, c.want)
		}
	}
	for _, lang := range []string{"go", "python", "javascript", "rust", "java", "cobol"} {
		if _, err := ParseCoverage(lang, "no report here"); err == nil {
			t.Errorf("%s: unparseable output accepted", lang)
		}
	}
}

func TestCoverageLevel(t *testing.T) {
	cases := []struct {
		name     string
		floor    int
		base     float64
		haveBase bool
		head     float64
		want     Level
	}{
		{"79 under 80", 80, 0, false, 79, LevelFail},
		{"80 meets 80", 80, 0, false, 80, LevelPass},
		{"base 70 head 72", 80, 70, true, 72, LevelPass},
		{"base 70 head 68", 80, 70, true, 68, LevelFail},
		{"base 70 head 70", 80, 70, true, 70, LevelPass},
		{"base 90 head 79", 80, 90, true, 79, LevelFail},
	}
	for _, c := range cases {
		if got := CoverageLevel(c.floor, c.base, c.haveBase, c.head); got != c.want {
			t.Errorf("%s: %s", c.name, got)
		}
	}
}

func TestCoverageCommand(t *testing.T) {
	for _, lang := range []string{"go", "python", "rust"} {
		if c, ok := CoverageCommand(lang, ""); !ok || c == "" {
			t.Errorf("%s: no default command", lang)
		}
	}
	if c, ok := CoverageCommand("javascript", "npm test"); !ok || c == "npm test" {
		t.Errorf("javascript wraps the test command: %q %v", c, ok)
	}
	if _, ok := CoverageCommand("javascript", ""); ok {
		t.Error("javascript without a test command has a coverage command")
	}
	if _, ok := CoverageCommand("cobol", "x"); ok {
		t.Error("unknown language has a coverage command")
	}
}
