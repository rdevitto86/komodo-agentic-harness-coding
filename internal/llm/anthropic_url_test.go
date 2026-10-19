package llm

import (
	"strings"
	"testing"
)

func TestAnthropicBaseURLAllowed(t *testing.T) {
	for _, u := range []string{
		"", "https://api.anthropic.com", "https://proxy.example.com/", "http://127.0.0.1:8080",
		"http://[::1]:8080", "http://localhost:9000", "HTTP://LocalHost",
	} {
		if _, err := NewAnthropic(AnthropicOptions{BaseURL: u, Key: apiKey}); err != nil {
			t.Errorf("%q refused: %v", u, err)
		}
	}
}

func TestAnthropicBaseURLRefused(t *testing.T) {
	bad := map[string]string{
		"http://api.example.com":   "plain http",
		"http://10.0.0.5:8080":     "plain http",
		"http://localhost.evil.io": "plain http",
		"ftp://api.example.com":    "https",
		"api.example.com":          "absolute",
		"//api.example.com":        "absolute",
		"https://":                 "host",
		"https://exa mple.com":     "absolute",
	}

	for u, want := range bad {
		_, err := NewAnthropic(AnthropicOptions{BaseURL: u, Key: apiKey})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want it to mention %q", u, err, want)
		}

		if err := CheckBaseURL(u); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("CheckBaseURL(%q) = %v, want it to mention %q", u, err, want)
		}
	}
}
