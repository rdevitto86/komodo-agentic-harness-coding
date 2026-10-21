package config

import "testing"

func TestCoverageFloorDefaultsToNinety(t *testing.T) {
	m, _ := newTestManager(t)
	it, err := m.GetConfigItem("check.coverage")
	if err != nil {
		t.Fatal(err)
	}
	if it.Default != 90 || it.Value != 90 {
		t.Fatalf("check.coverage default %v, value %v; want 90", it.Default, it.Value)
	}
}
