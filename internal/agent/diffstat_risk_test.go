package agent

import "testing"

// riskOf classifies a diff by its files alone.
func riskOf(diff string) RiskDecision { return Classify(RiskInput{Files: DiffFiles(diff)}) }

// A pure rename of a Makefile has no hunk, yet it is critical beside a README edit.
func TestDiffFilesSeesAPureRenameByBothPaths(t *testing.T) {
	diff := "diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-a\n+b\n" +
		"diff --git a/Makefile b/build.txt\nsimilarity index 100%\nrename from Makefile\nrename to build.txt\n"

	if d := riskOf(diff); d.Level != RiskCritical {
		t.Errorf("rename of Makefile = %+v, want critical", d)
	}

	diff = "diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-a\n+b\n" +
		"diff --git a/notes.txt b/Makefile\nsimilarity index 100%\nrename from notes.txt\nrename to Makefile\n"
	if d := riskOf(diff); d.Level != RiskCritical {
		t.Errorf("rename to Makefile = %+v, want critical", d)
	}
}

// A binary file has no hunk; it is at least medium, whatever its name looks like.
func TestDiffFilesSeesABinaryFile(t *testing.T) {
	diff := "diff --git a/docs/x.md b/docs/x.md\nnew file mode 100644\nindex 0000000..1111111\n" +
		"Binary files /dev/null and b/docs/x.md differ\n"

	files := DiffFiles(diff)
	if len(files) != 1 || files[0].Path != "docs/x.md" || !files[0].Binary {
		t.Fatalf("files = %+v", files)
	}
	if d := riskOf(diff); d.Level.Rank() < RiskMedium.Rank() {
		t.Errorf("binary = %+v, want at least medium", d)
	}
}

// Deleting a workflow file is critical, and so is a mode change with no hunk.
func TestDiffFilesSeesADeletionAndAModeChange(t *testing.T) {
	del := "diff --git a/.github/workflows/ci.yml b/.github/workflows/ci.yml\ndeleted file mode 100644\n" +
		"--- a/.github/workflows/ci.yml\n+++ /dev/null\n@@ -1 +0,0 @@\n-on: push\n"
	if d := riskOf(del); d.Level != RiskCritical {
		t.Errorf("deleted workflow = %+v, want critical", d)
	}

	mode := "diff --git a/Makefile b/Makefile\nold mode 100644\nnew mode 100755\n"
	if d := riskOf(mode); d.Level != RiskCritical {
		t.Errorf("mode change = %+v, want critical", d)
	}
}
