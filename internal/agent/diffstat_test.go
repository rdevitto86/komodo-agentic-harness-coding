package agent

import (
	"slices"
	"testing"
)

// The changed files and their line counts come from the unified diff, including new and deleted files.
func TestDiffFiles(t *testing.T) {
	diff := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,3 @@\n keep\n-old\n+new\n+more\n" +
		"diff --git a/n.md b/n.md\nnew file mode 100644\n--- /dev/null\n+++ b/n.md\n@@ -0,0 +1,2 @@\n+x\n+y\n" +
		"diff --git a/gone.go b/gone.go\ndeleted file mode 100644\n--- a/gone.go\n+++ /dev/null\n@@ -1,1 +0,0 @@\n-bye\n"

	got := DiffFiles(diff)
	want := []ChangedFile{{"a.go", 2, 1}, {"n.md", 2, 0}, {"gone.go", 0, 1}}

	if !slices.Equal(got, want) {
		t.Errorf("DiffFiles = %+v, want %+v", got, want)
	}
	if DiffFiles("") != nil {
		t.Error("empty diff has files")
	}
}
