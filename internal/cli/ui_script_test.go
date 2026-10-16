package cli

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// smokeResult is what testdata/ui_smoke.js prints.
type smokeResult struct {
	ErrorKids          int      `json:"errorKids"`
	ErrorKidsWithError int      `json:"errorKidsWithError"`
	RunTexts           []string `json:"runTexts"`
	All                []string `json:"all"`
}

func TestUIScriptDrawsNoStrayTextOrDuplicateStage(t *testing.T) {
	osa, err := exec.LookPath("osascript")
	if err != nil {
		t.Skip("osascript is not installed; the script smoke test needs macOS JavaScript")
	}
	script, err := filepath.Abs(filepath.Join("testdata", "ui_smoke.js"))
	if err != nil {
		t.Fatal(err)
	}
	page, err := filepath.Abs(filepath.Join("..", "..", "embed", "ui", "ui.js"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(osa, "-l", "JavaScript", script, page).CombinedOutput()
	if err != nil {
		t.Fatalf("run the script: %v\n%s", err, out)
	}
	var r smokeResult
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatalf("smoke output %q: %v", out, err)
	}
	if r.ErrorKids != 0 || r.ErrorKidsWithError != 1 {
		t.Errorf("the error area held %d children without an error and %d with one", r.ErrorKids, r.ErrorKidsWithError)
	}
	if slices.Contains(r.All, "null") || slices.Contains(r.All, "undefined") {
		t.Errorf("the page drew stray text: %q", r.All)
	}
	for _, code := range []string{"test_review", "build", "intake"} {
		if slices.Contains(r.RunTexts, code) {
			t.Errorf("a group card shows the raw stage code %q beside its chip", code)
		}
	}
}
