package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/config"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
)

func TestBuildLinksCallsTheConfiguredAnthropicURL(t *testing.T) {
	hits := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- r.URL.Path
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer srv.Close()

	t.Setenv("ANTHROPIC_API_KEY", "sk-test-key")
	t.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1:1")

	cfg, err := config.New(t.TempDir(), config.Known{}, func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.SetConfigItem(config.Change{Key: "providers.anthropic.url", New: srv.URL}); err != nil {
		t.Fatal(err)
	}

	cat, err := llm.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}

	_, _ = buildLinks(cfg, cat, nil).Probe(context.Background(), llm.AnthropicAPI)

	select {
	case <-hits:
	default:
		t.Fatal("the paid adapter did not call providers.anthropic.url")
	}
}

func TestBuildLinksLeavesOutThePaidRouteForPlainHTTPToAnotherHost(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-key")

	cfg, err := config.New(t.TempDir(), config.Known{}, func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.SetConfigItem(
		config.Change{Key: "providers.anthropic.url", New: "http://api.example.com"},
	); err != nil {
		t.Fatal(err)
	}

	cat, err := llm.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}

	_, err = buildLinks(cfg, cat, nil).Probe(context.Background(), llm.AnthropicAPI)
	if !errors.Is(err, llm.ErrUnavailable) {
		t.Fatalf("paid route probe = %v, want ErrUnavailable", err)
	}
}

func TestBuildLinksOpenAIKeyAndModelsFromSettings(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-openai-test" {
			http.Error(w, "no key", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"gpt-6.1-sol"},{"id":"gpt-6-astra"},{"id":"claude-opus-5-5"}]}`)
	}))
	defer srv.Close()

	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("MY_OPENAI_KEY", "sk-openai-test")
	t.Setenv("OPENAI_BASE_URL", srv.URL)

	cfg, err := config.New(t.TempDir(), config.Known{}, func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	cat, err := llm.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := buildLinks(cfg, cat, nil).Probe(context.Background(), llm.OpenAIAPI); err == nil {
		t.Fatal("the default key variable is empty, so the paid OpenAI link must not be built")
	}
	if _, err := cfg.SetConfigItem(config.Change{Key: "providers.keys.openai", New: "env:MY_OPENAI_KEY"}); err != nil {
		t.Fatal(err)
	}

	res, err := buildLinks(cfg, cat, nil).Probe(context.Background(), llm.OpenAIAPI)
	if err != nil || !res.OK {
		t.Fatalf("Probe = %+v, %v", res, err)
	}
	if res.Models[llm.Standard] != "gpt-6.1-sol" || res.Models[llm.Heavy] != "gpt-6-astra" {
		t.Errorf("models = %v, want the catalog's OpenAI rows the key can use", res.Models)
	}
	for w, id := range res.Models {
		if id == "claude-opus-5-5" {
			t.Errorf("a Claude row was picked for %s", w)
		}
	}
}
