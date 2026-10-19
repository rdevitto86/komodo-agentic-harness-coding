package config

import "testing"

func TestAnthropicURLIsASetting(t *testing.T) {
	m, _ := newTestManager(t)

	it, err := m.GetConfigItem("providers.anthropic.url")
	if err != nil || it.Value != "https://api.anthropic.com" || it.Source != LayerBuiltIn {
		t.Fatalf("default: %+v, %v", it, err)
	}

	mustSet(t, m, "providers.anthropic.url", "https://proxy.example.com")

	it, err = m.GetConfigItem("providers.anthropic.url")
	if err != nil || it.Value != "https://proxy.example.com" || it.Source != LayerMachine {
		t.Fatalf("after set: %+v, %v", it, err)
	}
}
