package llm

import (
	"fmt"
	"strings"
)

// subscriptionTypes are the plans whose login counts as a subscription.
var subscriptionTypes = map[string]bool{"pro": true, "max": true, "team": true, "enterprise": true}

// Classify returns the cost class of a link from its login; unknown means paid.
// The local link is classed by its endpoint (see LocalEndpoint.CostClass), so here it fails closed.
func Classify(l Link, auth AuthStatus) string {
	if l == ClaudeCLI &&
		auth.AuthMethod == "claude.ai" &&
		subscriptionTypes[auth.SubscriptionType] {
		return CostSubscription
	}

	return CostPaid
}

// Scrub returns env without the variables that would make claude prefer an API key.
// CLAUDE_CODE_OAUTH_TOKEN stays: it is a subscription token.
func Scrub(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !scrubbed(name) {
			out = append(out, kv)
		}
	}

	return out
}

// CheckInit fails with ErrBilling unless the first stream line reports apiKeySource "none".
func CheckInit(i Init) error {
	if i.APIKeySource != "none" {
		return fmt.Errorf("apiKeySource is %q: %w", i.APIKeySource, ErrBilling)
	}

	return nil
}

// scrubbed reports whether an environment variable name would make claude prefer an API key.
func scrubbed(name string) bool {
	name = strings.ToUpper(name)
	switch name {
	case "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL":
		return true
	}

	return strings.HasPrefix(name, "CLAUDE_CODE_USE_")
}
