package agent

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

// toolInvocation matches one build-tool call at the start of a shell segment.
var toolInvocation = regexp.MustCompile(`^(?:then |else )?(\./gradlew|gradle|\./mvnw|mvn) `)

// failureTolerantTool reports whether every Gradle or Maven call in the shell command carries the flag that keeps the
// build going past a failing test; without it the real tools stop and write no report.
func failureTolerantTool(cmd, flag string) bool {
	calls := 0
	for _, seg := range strings.Split(cmd, ";") {
		seg = strings.TrimSpace(seg)
		if !toolInvocation.MatchString(seg) {
			continue
		}
		calls++
		if !strings.Contains(seg, flag) {
			return false
		}
	}
	return calls > 0
}

// A repository whose tests already fail at the base still gets its coverage report written by Gradle and Maven.
func TestJavaProfilesKeepGoingPastFailingTests(t *testing.T) {
	cases := []struct {
		name, marker, flag string
	}{
		{"gradle", "build.gradle", "--continue"},
		{"maven", "pom.xml", "-Dmaven.test.failure.ignore=true"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, ok := shippedTools(t).Resolve("java", has(c.marker), nil)
			if !ok || r.Name != c.name {
				t.Fatalf("resolve = %+v %v", r, ok)
			}
			for what, cmd := range map[string]string{"test": r.Test, "coverage": r.Coverage} {
				if !failureTolerantTool(cmd, c.flag) {
					t.Errorf("%s command stops at the first failing test: %q", what, cmd)
				}
			}

			// The fake tool answers like the real one: a report only when the call tolerates failures.
			run := func(_ context.Context, cmd string) (int, string, error) {
				switch cmd {
				case r.Test:
					return 1, "1 test failed", nil
				case r.JUnit:
					return 0, fixture(t, "junit-surefire.xml"), nil
				case r.Coverage:
					if !failureTolerantTool(cmd, c.flag) {
						return 0, "", nil
					}
					return 0, fixture(t, "jacoco-app.xml"), nil
				}
				return 0, "", nil
			}
			in := CheckInput{
				Group: "g", Base: "b", Head: "h", Language: "java",
				Files: []string{"A.java"}, Changed: []string{"A.java"},
			}
			cfg := CheckerConfig{
				CoverageFloor:    1,
				TestCommands:     map[string]string{"java": r.Test},
				ReportCommands:   map[string]string{"java": r.JUnit},
				CoverageCommands: map[string]string{"java": r.Coverage},
			}
			res, err := newTestChecker(cfg).Check(context.Background(), run, in)
			if err != nil {
				t.Fatalf("check: %v", err)
			}
			if s := step(t, res, StepCoverage); s.Level == LevelFail {
				t.Errorf("coverage = %+v", s)
			}
		})
	}
}

// Kotlin shares the Java entries, so it gets the same failure-tolerant commands.
func TestKotlinProfileKeepsGoingPastFailingTests(t *testing.T) {
	r, ok := shippedTools(t).Resolve("kotlin", has("build.gradle.kts"), nil)
	if !ok || !failureTolerantTool(r.Test, "--continue") || !failureTolerantTool(r.Coverage, "--continue") {
		t.Errorf("kotlin gradle commands = %+v %v", r, ok)
	}
}

// The Rust coverage command writes its report even when a test fails.
func TestRustCoverageCommandIgnoresFailingTests(t *testing.T) {
	c, ok := CoverageCommand("rust", "")
	if !ok || !strings.Contains(c, "--ignore-run-fail") {
		t.Errorf("rust coverage command = %q %v", c, ok)
	}
}
