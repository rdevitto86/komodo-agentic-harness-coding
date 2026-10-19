package llm

import "testing"

func TestCatalogProviderPicksOwnRows(t *testing.T) {
	c, _ := LoadCatalog()
	want := map[Weight]string{Heavy: "gpt-6-astra", Standard: "gpt-6.1-sol", Light: "gpt-6-luna"}
	for w, id := range want {
		e, ok := c.DefaultFor(ProviderOpenAI, w)
		if !ok || e.ID != id || e.Provider != ProviderOpenAI || e.Window != 1050000 || e.MaxOut != 128000 {
			t.Errorf("DefaultFor(openai, %s) = %+v %v, want %s", w, e, ok, id)
		}
	}
	for _, l := range []Link{OpenAIAPI, OpenAISubscription} {
		if got := ProviderOf(l); got != ProviderOpenAI {
			t.Errorf("ProviderOf(%s) = %q", l, got)
		}
		if got := c.ModelsFor(ProviderOf(l))[Standard]; got != "gpt-6.1-sol" {
			t.Errorf("%s standard = %q", l, got)
		}
	}
	for _, l := range []Link{ClaudeCLI, AnthropicAPI} {
		for w, id := range c.ModelsFor(ProviderOf(l)) {
			if e, _ := c.Lookup(id); e.Provider != ProviderAnthropic {
				t.Errorf("%s picked %s for %s", l, id, w)
			}
		}
	}
	for w, id := range c.Models() {
		if e, _ := c.Lookup(id); e.Provider != ProviderAnthropic {
			t.Errorf("Models()[%s] = %s, not an Anthropic row", w, id)
		}
	}
	if _, ok := c.Prior("gpt-6.1-sol"); ok {
		t.Error("OpenAI rows list no prior model")
	}
}

func TestParseCatalogProviderRules(t *testing.T) {
	two := `[{"id":"a","provider":"anthropic","weight":"heavy","tier":"latest"},` +
		`{"id":"b","provider":"openai","weight":"heavy","tier":"latest"}]`
	if _, err := ParseCatalog([]byte(two)); err != nil {
		t.Errorf("one latest per provider and weight: %v", err)
	}
	for name, in := range map[string]string{
		"no provider":  `[{"id":"a","weight":"heavy","tier":"latest"}]`,
		"bad provider": `[{"id":"a","provider":"acme","weight":"heavy","tier":"latest"}]`,
		"local tier":   `[{"id":"a","provider":"openai","weight":"heavy","tier":"local"}]`,
		"two latests": `[{"id":"a","provider":"openai","weight":"heavy","tier":"latest"},` +
			`{"id":"b","provider":"openai","weight":"heavy","tier":"latest"}]`,
	} {
		if _, err := ParseCatalog([]byte(in)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestOpenAIModelsFromCatalog(t *testing.T) {
	c, _ := LoadCatalog()
	m := c.OpenAIModels()
	if len(m) != 3 || m[Standard].ID != "gpt-6.1-sol" || m[Standard].PriceIn != 2 || m[Standard].PriceOut != 10 ||
		m[Standard].Window != 1050000 || m[Heavy].PriceOut != 50 || m[Light].PriceIn != 0.1 {
		t.Errorf("OpenAIModels = %+v", m)
	}
}
