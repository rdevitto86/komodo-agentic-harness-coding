package llm

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
)

// CatalogEntry is one model in models.json.
type CatalogEntry struct {
	ID       string `json:"id"`
	Provider string `json:"provider"` // anthropic, openai or local
	Weight   string `json:"weight"`   // heavy, standard or light
	Tier     string `json:"tier"`     // latest, prior or local
	// context tokens; absent for local models, which are measured
	Window   int     `json:"window,omitempty"`
	MaxOut   int     `json:"max_out,omitempty"`
	PriceIn  float64 `json:"price_in_usd_per_m"`
	PriceOut float64 `json:"price_out_usd_per_m"`
}

// The providers a catalog row names.
const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
	ProviderLocal     = "local"
)

// ProviderOf returns the provider whose catalog rows a link may run.
func ProviderOf(l Link) string {
	switch l {
	case OpenAIAPI, OpenAISubscription:
		return ProviderOpenAI
	case Local:
		return ProviderLocal
	}
	return ProviderAnthropic
}

// Catalog is the parsed model catalog.
type Catalog struct{ entries []CatalogEntry }

//go:embed models.json
var modelsJSON []byte

// weightOrder lists the weights from heaviest to lightest, for the nearest-weight rule.
var weightOrder = []Weight{Heavy, Standard, Light}

// Prior returns the prior model of the same provider and weight as the given latest identifier.
func (c *Catalog) Prior(id string) (CatalogEntry, bool) {
	cur, ok := c.Lookup(id)
	if !ok || cur.Tier != "latest" {
		return CatalogEntry{}, false
	}

	for _, e := range c.Entries() {
		if e.Tier == "prior" && e.Weight == cur.Weight && e.Provider == cur.Provider {
			return e, true
		}
	}

	return CatalogEntry{}, false
}

// Lookup finds an entry by its exact identifier.
func (c *Catalog) Lookup(id string) (CatalogEntry, bool) {
	for _, e := range c.Entries() {
		if e.ID == id {
			return e, true
		}
	}
	return CatalogEntry{}, false
}

// Entries returns a copy of every entry.
func (c *Catalog) Entries() []CatalogEntry {
	if c == nil {
		return nil
	}
	return slices.Clone(c.entries)
}

// LocalModel returns the local model of a weight, or the nearest weight that lists one.
func (c *Catalog) LocalModel(w Weight) (CatalogEntry, bool) {
	at := slices.Index(weightOrder, w)
	if at < 0 {
		return CatalogEntry{}, false
	}

	for d := 0; d < len(weightOrder); d++ {
		for _, i := range []int{at - d, at + d} {
			if i < 0 || i >= len(weightOrder) {
				continue
			}
			for _, e := range c.Entries() {
				if e.Tier == "local" && e.Weight == string(weightOrder[i]) {
					return e, true
				}
			}
		}
	}

	return CatalogEntry{}, false
}

// Models returns the latest Anthropic identifier per weight.
func (c *Catalog) Models() map[Weight]string { return c.ModelsFor(ProviderAnthropic) }

// ModelsFor returns the latest identifier per weight of one provider.
func (c *Catalog) ModelsFor(provider string) map[Weight]string {
	out := map[Weight]string{}
	for _, w := range weightOrder {
		if e, ok := c.DefaultFor(provider, w); ok {
			out[w] = e.ID
		}
	}
	return out
}

// OpenAIModels returns the latest OpenAI model per weight, in the form both OpenAI links take.
func (c *Catalog) OpenAIModels() map[Weight]OpenAIModel {
	out := map[Weight]OpenAIModel{}
	for _, w := range weightOrder {
		if e, ok := c.DefaultFor(ProviderOpenAI, w); ok {
			out[w] = OpenAIModel{ID: e.ID, Window: e.Window, MaxOut: e.MaxOut, PriceIn: e.PriceIn, PriceOut: e.PriceOut}
		}
	}
	return out
}

// Default returns the latest Anthropic model of a weight.
func (c *Catalog) Default(w Weight) (CatalogEntry, bool) { return c.DefaultFor(ProviderAnthropic, w) }

// DefaultFor returns the latest model of one provider and weight.
func (c *Catalog) DefaultFor(provider string, w Weight) (CatalogEntry, bool) {
	for _, e := range c.Entries() {
		if e.Tier == "latest" && e.Provider == provider && e.Weight == string(w) {
			return e, true
		}
	}
	return CatalogEntry{}, false
}

// NewModelCheck returns the function the Configuration Manager takes as its modelOK argument.
// A model passes when a successful probe found it, or when it is a catalog model and some probe succeeded.
func NewModelCheck(c *Catalog, probes ...ProbeResult) func(model string) bool {
	ok := map[string]bool{}
	anyOK := false
	for _, p := range probes {
		if !p.OK {
			continue
		}
		anyOK = true
		for _, m := range p.Models {
			ok[m] = true
		}
	}

	return func(model string) bool {
		if model == "" || !anyOK {
			return false
		}
		if ok[model] {
			return true
		}
		e, found := c.Lookup(model)
		return found && e.Tier != "local"
	}
}

// LoadCatalog parses the embedded catalog.
func LoadCatalog() (*Catalog, error) { return ParseCatalog(modelsJSON) }

// ParseCatalog parses catalog JSON and checks providers, weights, tiers, unique identifiers
// and one latest model per provider and weight.
func ParseCatalog(data []byte) (*Catalog, error) {
	var entries []CatalogEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse model catalog: %w", err)
	}

	seen := map[string]bool{}
	latest := map[string]bool{}

	for _, e := range entries {
		slot := e.Provider + "/" + e.Weight

		switch {
		case e.ID == "":
			return nil, fmt.Errorf("model catalog entry has no id")
		case seen[e.ID]:
			return nil, fmt.Errorf("model catalog lists %q twice", e.ID)
		case !slices.Contains([]string{ProviderAnthropic, ProviderOpenAI, ProviderLocal}, e.Provider):
			return nil, fmt.Errorf("model %q has provider %q", e.ID, e.Provider)
		case !slices.Contains([]string{"heavy", "standard", "light"}, e.Weight):
			return nil, fmt.Errorf("model %q has weight %q", e.ID, e.Weight)
		case !slices.Contains([]string{"latest", "prior", "local"}, e.Tier):
			return nil, fmt.Errorf("model %q has tier %q", e.ID, e.Tier)
		case (e.Tier == "local") != (e.Provider == ProviderLocal):
			return nil, fmt.Errorf("model %q has tier %q and provider %q", e.ID, e.Tier, e.Provider)
		case e.Tier == "latest" && latest[slot]:
			return nil, fmt.Errorf("model catalog has two latest %s models for weight %s", e.Provider, e.Weight)
		}

		seen[e.ID] = true
		if e.Tier == "latest" {
			latest[slot] = true
		}
	}

	return &Catalog{entries: entries}, nil
}
