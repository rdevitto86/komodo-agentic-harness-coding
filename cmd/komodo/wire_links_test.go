package main

import (
	"context"
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
