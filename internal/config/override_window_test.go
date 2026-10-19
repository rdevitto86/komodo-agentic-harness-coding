package config

import (
	"fmt"
	"testing"
)

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

func TestASavedOverrideWindowOutsideTheRangeFallsBackLikeAnyOtherSavedLimit(t *testing.T) {
	for _, saved := range []int{0, 29, 121, 1440} {
		m, home := newTestManager(t)
		writeFile(t, home, "settings.json", fmt.Sprintf(`{"limits.override_window_min": %d}`, saved))

		it, err := m.GetConfigItem("limits.override_window_min")
		if err != nil || it.Value != 60 || !it.Fallback {
			t.Errorf("saved %d: item %+v err %v", saved, it, err)
		}
		if err := m.Verify(); err == nil {
			t.Errorf("saved %d: the file passed strict reading", saved)
		}
	}

	m, home := newTestManager(t)
	writeFile(t, home, "settings.json", `{"limits.group_retries": 99999}`)

	it, err := m.GetConfigItem("limits.group_retries")
	if err != nil || !it.Fallback {
		t.Errorf("the precedent, an out-of-range group_retries: item %+v err %v", it, err)
	}
}

func TestOverrideWindowDefaultIsSixty(t *testing.T) {
	m, _ := newTestManager(t)
	if it, err := m.GetConfigItem("limits.override_window_min"); err != nil || it.Value != 60 {
		t.Fatalf("default %v err %v", it.Value, err)
	}
}
