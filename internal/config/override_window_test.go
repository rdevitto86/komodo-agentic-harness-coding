package config

import "testing"

func TestOverrideWindowRangeIsThirtyToOneTwenty(t *testing.T) {
	m, _ := newTestManager(t)
	for _, bad := range []int{0, 29, 121} {
		if _, err := m.SetConfigItem(Change{Key: "limits.override_window_min", New: bad}); err == nil {
			t.Errorf("limits.override_window_min %d was accepted", bad)
		}
	}
	for _, ok := range []int{30, 120} {
		mustSet(t, m, "limits.override_window_min", ok)
	}
	if it, _ := m.GetConfigItem("limits.override_window_min"); it.Value != 120 {
		t.Fatalf("value %v", it.Value)
	}
}

func TestOverrideWindowDefaultIsSixty(t *testing.T) {
	m, _ := newTestManager(t)
	if it, err := m.GetConfigItem("limits.override_window_min"); err != nil || it.Value != 60 {
		t.Fatalf("default %v err %v", it.Value, err)
	}
}
