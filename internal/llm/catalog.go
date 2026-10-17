package llm

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
)

// CatalogEntry is one model in models.json.
type CatalogEntry struct {
	ID     string `json:"id"`
	Weight string `json:"weight"` // heavy, standard or light
	Tier   string `json:"tier"`   // latest, prior or local
	// context tokens; absent for local models, which are measured
	Window   int     `json:"window,omitempty"`
	MaxOut   int     `json:"max_out,omitempty"`
	PriceIn  float64 `json:"price_in_usd_per_m"`
	PriceOut float64 `json:"price_out_usd_per_m"`
}

// Catalog is the parsed model catalog.
type Catalog struct{ entries []CatalogEntry }

//go:embed models.json
var modelsJSON []byte

// weightOrder lists the weights from heaviest to lightest, for the nearest-weight rule.
var weightOrder = []Weight{Heavy, Standard, Light}

// NewModelCheck returns the function the Configuration Manager takes as its modelOK argument.
// A model passes when a successful probe found it, or when it is a catalog model and some probe
// succeeded.
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

// Entries returns a copy of every entry.
func (c *Catalog) Entries() []CatalogEntry {
	if c == nil {
		return nil
	}

	return slices.Clone(c.entries)
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

// Default returns the latest hosted model of a weight.
func (c *Catalog) Default(w Weight) (CatalogEntry, bool) {
	for _, e := range c.Entries() {
		if e.Tier == "latest" && e.Weight == string(w) {
			return e, true
		}
	}

	return CatalogEntry{}, false
}

// Prior returns the prior model of the same weight as the given latest identifier.
func (c *Catalog) Prior(id string) (CatalogEntry, bool) {
	cur, ok := c.Lookup(id)
	if !ok || cur.Tier != "latest" {
		return CatalogEntry{}, false
	}

	for _, e := range c.Entries() {
		if e.Tier == "prior" && e.Weight == cur.Weight {
			return e, true
		}
	}

	return CatalogEntry{}, false
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

// Models returns the latest hosted identifier per weight.
func (c *Catalog) Models() map[Weight]string {
	out := map[Weight]string{}

	for _, w := range weightOrder {
		if e, ok := c.Default(w); ok {
			out[w] = e.ID
		}
	}

	return out
}

// LoadCatalog parses the embedded catalog.
func LoadCatalog() (*Catalog, error) { return ParseCatalog(modelsJSON) }

// ParseCatalog parses catalog JSON and checks weights, tiers, unique identifiers and one latest
// model per weight.
func ParseCatalog(data []byte) (*Catalog, error) {
	var entries []CatalogEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse model catalog: %w", err)
	}

	seen := map[string]bool{}
	latest := map[string]bool{}

	for _, e := range entries {
		switch {
		case e.ID == "":
			return nil, fmt.Errorf("model catalog entry has no id")
		case seen[e.ID]:
			return nil, fmt.Errorf("model catalog lists %q twice", e.ID)
		case !slices.Contains([]string{"heavy", "standard", "light"}, e.Weight):
			return nil, fmt.Errorf("model %q has weight %q", e.ID, e.Weight)
		case !slices.Contains([]string{"latest", "prior", "local"}, e.Tier):
			return nil, fmt.Errorf("model %q has tier %q", e.ID, e.Tier)
		case e.Tier == "latest" && latest[e.Weight]:
			return nil, fmt.Errorf("model catalog has two latest models for weight %s", e.Weight)
		}

		seen[e.ID] = true
		if e.Tier == "latest" {
			latest[e.Weight] = true
		}
	}

	return &Catalog{entries: entries}, nil
}
