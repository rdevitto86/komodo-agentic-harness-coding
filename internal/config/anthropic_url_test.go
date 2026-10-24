package config

import (
	"errors"
	"testing"
)

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

func TestAnthropicURLRefusesAValueThatIsNotAnAbsoluteURL(t *testing.T) {
	m, _ := newTestManager(t)

	for _, v := range []string{
		"not a url", "api.example.com", "/v1", "//api.example.com", "https://", "https://exa mple.com", "mailto:a@b.c",
	} {
		_, err := m.SetConfigItem(Change{Key: "providers.anthropic.url", New: v})

		var bad BadValueError
		if !errors.As(err, &bad) || bad.Key != "providers.anthropic.url" {
			t.Errorf("%q: err = %v, want a BadValueError", v, err)
		}
	}

	it, _ := m.GetConfigItem("providers.anthropic.url")
	if it.Value != "https://api.anthropic.com" {
		t.Errorf("a refused value was stored: %+v", it)
	}
}

func TestAnthropicURLAcceptsAddressesAndEmpty(t *testing.T) {
	m, _ := newTestManager(t)

	for _, v := range []string{"http://127.0.0.1:8080", "http://[::1]:1", "https://proxy.example.com/base", ""} {
		if _, err := m.SetConfigItem(Change{Key: "providers.anthropic.url", New: v}); err != nil {
			t.Errorf("%q refused: %v", v, err)
		}
	}
}
