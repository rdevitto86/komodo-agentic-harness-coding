package main

import (
	"slices"
	"testing"

	komodo "github.com/rdevitto86/komodo-agentic-harness-coding"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/contextmgr"
)

// A Gradle or Maven repository has no detected test command; the language profile supplies the commands by build file.
func TestResolveToolsFillsJavaAndKotlinFromTheBuildFile(t *testing.T) {
	profiles, err := agent.LoadToolProfiles(komodo.Embedded)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		files []string
		lang  string
		tool  string
	}{
		{[]string{"build.gradle.kts", "src/main/kotlin/A.kt"}, "kotlin", "gradle"},
		{[]string{"build.gradle", "src/main/java/A.java"}, "java", "gradle"},
		{[]string{"pom.xml", "src/main/java/A.java"}, "java", "maven"},
	} {
		tree := listTree(tc.files)
		cmds, lang := commandTable(contextmgr.Detect(tree))
		lang, ts := resolveTools(profiles, tree, cmds, lang)
		if lang != tc.lang || ts.Name != tc.tool {
			t.Errorf("%v: language %q tool %q", tc.files, lang, ts.Name)
		}
		if len(cmds["test"]) != 3 || cmds["test"][0] != "sh" || ts.JUnit == "" || ts.Coverage == "" {
			t.Errorf("%v: test %v setup %+v", tc.files, cmds["test"], ts)
			continue
		}
		if ts.Test != cmds["test"][2] {
			t.Errorf("%v: baseline test command %q", tc.files, ts.Test)
		}
	}
}

// A repository that already has a test command keeps it, and a tool without a confirmed report gets no report command.
func TestResolveToolsKeepsDetectedCommands(t *testing.T) {
	profiles, err := agent.LoadToolProfiles(komodo.Embedded)
	if err != nil {
		t.Fatal(err)
	}

	tree := listTree{"Cargo.toml", "src/lib.rs"}
	cmds, lang := commandTable(contextmgr.Detect(tree))
	_, ts := resolveTools(profiles, tree, cmds, lang)
	if !slices.Equal(cmds["test"], []string{"cargo", "test"}) || ts.JUnit != "" || ts.Test != "cargo test" {
		t.Errorf("rust: %v %+v", cmds["test"], ts)
	}

	tree = listTree{"pyproject.toml", "a.py"}
	cmds, lang = commandTable(contextmgr.Detect(tree))
	if _, ts = resolveTools(profiles, tree, cmds, lang); ts.JUnit == "" || ts.Test != "pytest" {
		t.Errorf("python: %+v", ts)
	}
}

// package.json repositories are TypeScript to the coverage tools, which have no "node" entry.
func TestNodeRepositoriesUseACoverageLanguage(t *testing.T) {
	_, lang := commandTable(contextmgr.Detect(listTree{"package.json"}))
	if _, ok := agent.CoverageCommand(lang, "npm test"); !ok {
		t.Errorf("language %q has no coverage command", lang)
	}
}
