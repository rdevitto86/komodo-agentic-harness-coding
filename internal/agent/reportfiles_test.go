package agent

import (
	"context"
	"math"
	"strings"
	"testing"
)

// Gradle writes one report file per test class; the report command prints them one after another.
func TestParseJUnitReadsConcatenatedGradleReports(t *testing.T) {
	runs, err := ParseJUnit(fixture(t, "junit-gradle-a.xml") + fixture(t, "junit-gradle-b.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 4 {
		t.Fatalf("runs = %+v", runs)
	}

	failed := 0
	for _, r := range runs {
		if !r.Passed {
			failed++
			if r.Key() != "com.example.CartTest.rejectsNegativeQuantity()" || !strings.Contains(r.Output, "CartTest.java:31") {
				t.Errorf("failure = %+v", r)
			}
		}
	}
	if failed != 1 {
		t.Errorf("failed = %d", failed)
	}
}

// Surefire marks a crashed test with an error element, which fails the test like a failure does.
func TestParseJUnitReadsASurefireReport(t *testing.T) {
	runs, err := ParseJUnit(fixture(t, "junit-surefire.xml"))
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs = %+v %v", runs, err)
	}
	if !runs[0].Passed || runs[1].Passed || !strings.Contains(runs[1].Output, "FileNotFoundException") {
		t.Errorf("runs = %+v", runs)
	}
}

// The report-level LINE counter of a real JaCoCo report is the last one; package and class counters come first.
func TestParseCoverageReadsAJaCoCoReport(t *testing.T) {
	got, err := ParseCoverage("java", fixture(t, "jacoco-app.xml"))
	if err != nil || got != 80 {
		t.Fatalf("coverage = %v %v", got, err)
	}
}

// A multi-module build prints one report per module; their lines add up before the percent is taken.
func TestParseCoverageAddsTheReportsOfEveryModule(t *testing.T) {
	got, err := ParseCoverage("kotlin", fixture(t, "jacoco-app.xml")+fixture(t, "jacoco-lib.xml"))
	if err != nil || math.Abs(got-26.0*100/30) > 1e-9 {
		t.Fatalf("coverage = %v %v", got, err)
	}
}

// A Java repository that reports below the floor fails the coverage step through the profile command.
func TestCheckUsesTheProfileCoverageCommandForJava(t *testing.T) {
	cfg := CheckerConfig{
		CoverageFloor:    90,
		TestCommands:     map[string]string{"java": "gradle test"},
		CoverageCommands: map[string]string{"java": "gradle jacocoTestReport; cat jacoco.xml"},
		ReportCommands:   map[string]string{"java": "cat results.xml"},
	}
	sb := newSandbox().
		on("gradle test", reply{0, "BUILD SUCCESSFUL"}).
		on("cat results.xml", reply{0, fixture(t, "junit-gradle-b.xml")}).
		on("jacocoTestReport", reply{0, fixture(t, "jacoco-app.xml")})

	in := CheckInput{Group: "g", Base: "b", Head: "h", Language: "java", Files: []string{"A.java"}, Changed: []string{"A.java"}}

	r, err := newTestChecker(cfg).Check(context.Background(), sb.run, in)
	if err != nil {
		t.Fatal(err)
	}
	if s := step(t, r, StepCoverage); s.Level != LevelFail || !strings.Contains(s.Detail[0], "80.0%") {
		t.Errorf("coverage = %+v", s)
	}
}
