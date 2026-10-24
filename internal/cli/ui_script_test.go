package cli

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// smokeResult is what testdata/ui_smoke.js prints.
type smokeResult struct {
	ErrorKids          int      `json:"errorKids"`
	ErrorKidsWithError int      `json:"errorKidsWithError"`
	RunTexts           []string `json:"runTexts"`
	All                []string `json:"all"`
	DrawerText         []string `json:"drawerText"`
	Opened             bool     `json:"opened"`
	FocusInDrawer      bool     `json:"focusInDrawer"`
	LiveUpdate         bool     `json:"liveUpdate"`
	Closed             bool     `json:"closed"`
	FocusBack          bool     `json:"focusBack"`
	EnterOpens         bool     `json:"enterOpens"`
	BackdropCloses     bool     `json:"backdropCloses"`
	ClickOpens         bool     `json:"clickOpens"`
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
	if !r.Opened || !r.FocusInDrawer || !r.LiveUpdate || !r.Closed || !r.FocusBack {
		t.Errorf("the drawer keyboard flow failed: %+v", r)
	}
	if !r.EnterOpens || !r.BackdropCloses || !r.ClickOpens {
		t.Errorf("the drawer did not open on Enter and click or close on the backdrop: %+v", r)
	}
	for _, want := range []string{"plan-7f3a9c", "r1", "risky_change: ", "Approve the lease change?"} {
		if !slices.ContainsFunc(r.DrawerText, func(s string) bool { return strings.Contains(s, want) }) {
			t.Errorf("the drawer lacks %q: %q", want, r.DrawerText)
		}
	}
}
