package main

import "testing"

// A non-Go language builds and lints with its own marker's commands; a Makefile target is never used.
func TestLanguageChecksUseTheMarkersCommandsNotTheMakefile(t *testing.T) {
	build, lint := languageChecks(listTree{"Cargo.toml", "Makefile", "src/lib.rs"}, "rust")
	if build["rust"] != "cargo build" || lint["rust"] != "cargo clippy" {
		t.Errorf("rust: build %v lint %v", build, lint)
	}

	build, lint = languageChecks(listTree{"pyproject.toml", "a.py"}, "python")
	if len(build) != 0 || lint["python"] != "ruff check" {
		t.Errorf("python: build %v lint %v", build, lint)
	}
}

// Go and a language with no marker command add nothing: the checker builds and lints Go itself.
func TestLanguageChecksLeaveGoAndUnknownLanguagesToTheChecker(t *testing.T) {
	for lang, tree := range map[string]listTree{"go": {"go.mod", "a.go"}, "java": {"pom.xml", "A.java"}} {
		if build, lint := languageChecks(tree, lang); len(build) != 0 || len(lint) != 0 {
			t.Errorf("%s: build %v lint %v", lang, build, lint)
		}
	}
}
