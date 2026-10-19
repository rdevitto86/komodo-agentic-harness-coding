package config

import "testing"

func TestASettingsFileWithRemovedKeysStillLoads(t *testing.T) {
	m, home := newTestManager(t)
	writeFile(
		t, home, "settings.json",
		`{"limits.override_window_min": 45, "limits.override_retries": 2, "limits.group_retries": 2}`,
	)

	if err := m.resolve().readErr; err != nil {
		t.Fatalf("a file holding removed keys failed strict reading: %v", err)
	}
	if it, err := m.GetConfigItem("limits.group_retries"); err != nil || it.Value != 2 || it.Source != LayerMachine {
		t.Fatalf("the other keys of the file were not kept: %+v err %v", it, err)
	}
	if _, err := m.GetConfigItem("limits.override_window_min"); err == nil {
		t.Fatal("a removed key is still a setting")
	}
}

func TestAnUnknownKeyIsStillRefused(t *testing.T) {
	m, home := newTestManager(t)
	writeFile(t, home, "settings.json", `{"limits.override_window": 45}`)

	if err := m.resolve().readErr; err == nil {
		t.Fatal("a misspelled key passed strict reading")
	}
}
