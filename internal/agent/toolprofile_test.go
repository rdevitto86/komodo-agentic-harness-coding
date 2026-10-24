package agent

import (
	"os"
	"strings"
	"testing"
	"testing/fstest"

	komodo "github.com/rdevitto86/komodo-agentic-harness-coding"
)

// has returns a marker lookup that finds exactly the named files.
func has(files ...string) func(string) bool {
	return func(p string) bool {
		for _, f := range files {
			if f == p {
				return true
			}
		}
		return false
	}
}

// fixture reads a file of testdata.
func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func shippedTools(t *testing.T) ToolProfiles {
	t.Helper()
	p, err := LoadToolProfiles(komodo.Embedded)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A Gradle build file picks the Gradle commands: the test results folder and the JaCoCo report path from the tool docs.
func TestGradleProfileShipsReportAndCoverageCommands(t *testing.T) {
	r, ok := shippedTools(t).Resolve("java", has("build.gradle"), nil)
	if !ok || r.Name != "gradle" {
		t.Fatalf("resolve = %+v %v", r, ok)
	}
	if !strings.Contains(r.JUnit, "build/test-results/test/") {
		t.Errorf("junit command = %q", r.JUnit)
	}
	if !strings.Contains(r.Coverage, "jacocoTestReport") ||
		!strings.Contains(r.Coverage, "build/reports/jacoco/test/jacocoTestReport.xml") {
		t.Errorf("coverage command = %q", r.Coverage)
	}
	if !strings.Contains(r.Test, "gradlew") || !strings.Contains(r.Test, "gradle test") {
		t.Errorf("test command = %q", r.Test)
	}
}

// A Maven pom picks the Maven commands: the surefire reports folder and the JaCoCo report goal.
func TestMavenProfileShipsReportAndCoverageCommands(t *testing.T) {
	r, ok := shippedTools(t).Resolve("java", has("pom.xml"), nil)
	if !ok || r.Name != "maven" {
		t.Fatalf("resolve = %+v %v", r, ok)
	}
	if !strings.Contains(r.JUnit, "target/surefire-reports/") {
		t.Errorf("junit command = %q", r.JUnit)
	}
	if !strings.Contains(r.Coverage, "jacoco:report") || !strings.Contains(r.Coverage, "target/site/jacoco/jacoco.xml") {
		t.Errorf("coverage command = %q", r.Coverage)
	}
}

// Gradle wins over Maven when a repository has both build files, and Kotlin reads the Java entries.
func TestKotlinUsesTheJavaBuildToolsAndGradleWins(t *testing.T) {
	r, ok := shippedTools(t).Resolve("kotlin", has("build.gradle.kts", "pom.xml"), nil)
	if !ok || r.Name != "gradle" {
		t.Fatalf("resolve = %+v %v", r, ok)
	}
	if _, ok := shippedTools(t).Resolve("java", has("README.md"), nil); ok {
		t.Error("a repository without a build file got a profile")
	}
}

// A repository's own test command is kept, and a JUnit command applies only to the command it was written for.
func TestReportCommandAppliesOnlyToItsOwnTestCommand(t *testing.T) {
	tools := shippedTools(t)

	r, ok := tools.Resolve("python", has("pyproject.toml"), []string{"pytest"})
	if !ok || r.JUnit == "" || !strings.Contains(r.JUnit, "--junit-xml") {
		t.Fatalf("pytest: %+v %v", r, ok)
	}

	r, ok = tools.Resolve("python", has("pyproject.toml"), []string{"make", "test"})
	if !ok || r.JUnit != "" {
		t.Fatalf("make test must get no report command: %+v %v", r, ok)
	}
	if r.Test != "" {
		t.Errorf("a detected test command must not be replaced: %q", r.Test)
	}
}

// A tool the docs do not confirm ships no JUnit command, so its failures stay unclassifiable.
func TestUnconfirmedToolsShipNoReportCommand(t *testing.T) {
	tools := shippedTools(t)
	if _, ok := tools.Resolve("rust", has("Cargo.toml"), []string{"cargo", "test"}); ok {
		if r, _ := tools.Resolve("rust", has("Cargo.toml"), []string{"cargo", "test"}); r.JUnit != "" {
			t.Errorf("rust junit = %q", r.JUnit)
		}
	}
	if r, ok := tools.Resolve("typescript", has("package.json"), []string{"npm", "test"}); ok && r.JUnit != "" {
		t.Errorf("typescript junit = %q", r.JUnit)
	}
}

// The loader reads only the tools key, and a file without it adds nothing.
func TestLoadToolProfilesReadsOnlyTools(t *testing.T) {
	fsys := fstest.MapFS{
		"embed/langs/zig.json": {Data: []byte(`{"extensions": [".zig"], "symbols": []}`)},
		"embed/langs/foo.json": {Data: []byte(`{"extensions": [".foo"], "tools": [` +
			`{"name": "foo", "markers": ["foo.toml"], "test": "foo test", "junit": "foo report"}]}`)},
	}
	p, err := LoadToolProfiles(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := p.Resolve("foo", has("foo.toml"), nil); !ok || r.Test != "foo test" || r.JUnit != "foo report" {
		t.Errorf("foo = %+v %v", r, ok)
	}
	if _, ok := p.Resolve("zig", has("zig.toml"), nil); ok {
		t.Error("zig has no tools")
	}
}
