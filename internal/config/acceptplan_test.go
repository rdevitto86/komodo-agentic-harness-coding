package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// AcceptPlan names the machine files whose checksum differs and writes nothing.
func TestAcceptPlanListsDriftWithoutWriting(t *testing.T) {
	m, home := newTestManager(t)
	mustSet(t, m, "limits.returns", 4)
	if got, err := m.AcceptPlan(); err != nil || len(got) != 0 {
		t.Fatalf("clean files: %v %v", got, err)
	}
	writeFile(t, home, "settings.json", `{"limits.returns": 2}`)
	shaBefore, _ := os.ReadFile(filepath.Join(home, "state", "config.sha"))
	got, err := m.AcceptPlan()
	if err != nil || len(got) != 1 || got[0].Key != "settings.json" || got[0].Old == got[0].New || got[0].New == "" {
		t.Fatalf("AcceptPlan: %+v %v", got, err)
	}
	shaAfter, _ := os.ReadFile(filepath.Join(home, "state", "config.sha"))
	if string(shaBefore) != string(shaAfter) {
		t.Fatal("AcceptPlan wrote config.sha")
	}
	if err := m.Verify(); err == nil {
		t.Fatal("AcceptPlan accepted the edit")
	}
	b, err := m.Accept()
	if err != nil || !reflect.DeepEqual(b.Changes, got) {
		t.Fatalf("Accept disagrees with AcceptPlan: %+v %v", b, err)
	}
}

// AcceptPlan reports a file with no recorded checksum, and nothing when no machine file exists.
func TestAcceptPlanUnrecordedAndEmpty(t *testing.T) {
	m, home := newTestManager(t)
	if got, err := m.AcceptPlan(); err != nil || len(got) != 0 {
		t.Fatalf("no files: %v %v", got, err)
	}
	writeFile(t, home, "agents.json", `{}`)
	got, err := m.AcceptPlan()
	if err != nil || len(got) != 1 || got[0].Key != "agents.json" || got[0].Old != "" || got[0].New == "" {
		t.Fatalf("unrecorded file: %+v %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(home, "state", "config.sha")); !os.IsNotExist(err) {
		t.Fatal("AcceptPlan created config.sha")
	}
}
