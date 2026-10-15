package config

import (
	"errors"
	"testing"
)

// events.retention_days defaults to 30 and refuses 0; limits.usd_ceiling_per_day defaults to 0, meaning off.
func TestEventStoreKeys(t *testing.T) {
	m, _ := newTestManager(t)
	for key, want := range map[string]int{"events.retention_days": 30, "limits.usd_ceiling_per_day": 0, "metrics.retention_days": 30} {
		it, err := m.GetConfigItem(key)
		if err != nil || it.Value != want {
			t.Fatalf("%s = %+v, %v, want %d", key, it, err, want)
		}
	}
	var le LimitError
	if _, err := m.SetConfigItem(Change{Key: "events.retention_days", New: 0}); !errors.As(err, &le) {
		t.Fatalf("events.retention_days=0: want LimitError, got %v", err)
	}
	var bv BadValueError
	if _, err := m.SetConfigItem(Change{Key: "limits.usd_ceiling_per_day", New: "lots"}); !errors.As(err, &bv) {
		t.Fatalf("usd_ceiling_per_day=lots: want BadValueError, got %v", err)
	}
	mustSet(t, m, "events.retention_days", 7, "limits.usd_ceiling_per_day", 25)
	if it, _ := m.GetConfigItem("events.retention_days"); it.Value != 7 {
		t.Fatalf("events.retention_days = %+v", it)
	}
}
