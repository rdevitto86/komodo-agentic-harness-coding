package llm

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	sub := AuthStatus{AuthMethod: "claude.ai", SubscriptionType: "max"}
	cases := []struct {
		name string
		l    Link
		auth AuthStatus
		want string
	}{
		{"subscription max", ClaudeCLI, sub, CostSubscription},
		{"subscription pro", ClaudeCLI, AuthStatus{AuthMethod: "claude.ai", SubscriptionType: "pro"}, CostSubscription},
		{"enterprise", ClaudeCLI, AuthStatus{AuthMethod: "claude.ai", SubscriptionType: "enterprise"}, CostSubscription},
		{"logged out is paid", ClaudeCLI, AuthStatus{}, CostPaid},
		{"api key login is paid", ClaudeCLI, AuthStatus{AuthMethod: "api_key", SubscriptionType: "max"}, CostPaid},
		{"unknown plan is paid", ClaudeCLI, AuthStatus{AuthMethod: "claude.ai", SubscriptionType: "free"}, CostPaid},
		{"anthropic api", AnthropicAPI, sub, CostPaid},
		{"openai api", OpenAIAPI, sub, CostPaid},
		{"unknown link", Link("mystery"), sub, CostPaid},
		{"local without endpoint facts", Local, sub, CostPaid},
	}
	for _, c := range cases {
		if got := Classify(c.l, c.auth); got != c.want {
			t.Errorf("%s: Classify = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestScrub(t *testing.T) {
	env := []string{
		"PATH=/bin", "ANTHROPIC_API_KEY=sk-1", "ANTHROPIC_AUTH_TOKEN=t", "ANTHROPIC_BASE_URL=http://x",
		"CLAUDE_CODE_USE_BEDROCK=1", "CLAUDE_CODE_USE_VERTEX=1", "CLAUDE_CODE_OAUTH_TOKEN=keep", "HOME=/h",
	}
	got := Scrub(env)
	want := []string{"PATH=/bin", "CLAUDE_CODE_OAUTH_TOKEN=keep", "HOME=/h"}
	if !slices.Equal(got, want) {
		t.Fatalf("Scrub = %v, want %v", got, want)
	}
	if len(env) != 8 {
		t.Fatalf("Scrub changed its input")
	}
}

func TestCheckInit(t *testing.T) {
	if err := CheckInit(Init{APIKeySource: "none"}); err != nil {
		t.Fatalf("none: %v", err)
	}
	for _, src := range []string{"ANTHROPIC_API_KEY", "", "apiKeyHelper"} {
		err := CheckInit(Init{APIKeySource: src})
		if !errors.Is(err, ErrBilling) {
			t.Errorf("source %q: err = %v, want ErrBilling", src, err)
		}
	}
	if err := CheckInit(Init{APIKeySource: "ANTHROPIC_API_KEY"}); err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Errorf("error should name the source: %v", err)
	}
}
