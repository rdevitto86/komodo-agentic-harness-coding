package llm

import (
	"context"
	"errors"
	"testing"
)

func TestCatalogLoads(t *testing.T) {
	c, err := LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Entries()) == 0 {
		t.Fatal("no entries")
	}
	for w, want := range map[Weight]string{
		Heavy:    "claude-opus-5-5",
		Standard: "claude-sonnet-5-5",
		Light:    "claude-haiku-5-5",
	} {
		e, ok := c.Default(w)
		if !ok || e.ID != want || e.Tier != "latest" {
			t.Errorf("Default(%s) = %+v %v, want %s", w, e, ok, want)
		}
		if e.Window <= 0 {
			t.Errorf("%s has no window", e.ID)
		}
	}
	if got := c.Models(); got[Heavy] != "claude-opus-5-5" || len(got) != 3 {
		t.Errorf("Models = %v", got)
	}
}

func TestCatalogPriorStaysInLine(t *testing.T) {
	c, _ := LoadCatalog()
	p, ok := c.Prior("claude-sonnet-5-5")
	if !ok || p.ID != "claude-sonnet-5" || p.Tier != "prior" {
		t.Fatalf("Prior = %+v %v", p, ok)
	}
	if _, ok := c.Prior("claude-sonnet-5"); ok {
		t.Error("a prior model has no prior")
	}
	if _, ok := c.Prior("nope"); ok {
		t.Error("unknown id has no prior")
	}
}

func TestCatalogNoAliasesAndLocalFree(t *testing.T) {
	c, _ := LoadCatalog()
	for _, e := range c.Entries() {
		if e.Tier == "local" {
			if e.Window != 0 || e.PriceIn != 0 || e.PriceOut != 0 {
				t.Errorf("local model %s lists a window or price", e.ID)
			}
			continue
		}
		if e.ID == "opus" || e.ID == "sonnet" || e.ID == "haiku" || e.Window == 0 {
			t.Errorf("hosted entry %q must be an exact id with a window", e.ID)
		}
	}
}

func TestLocalModelNearestWeight(t *testing.T) {
	c, err := ParseCatalog(
		[]byte(`[{"id":"only-light","provider":"local","weight":"light","tier":"local",`+
			`"price_in_usd_per_m":0,"price_out_usd_per_m":0}]`),
	)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := c.LocalModel(Heavy)
	if !ok || e.ID != "only-light" {
		t.Fatalf("LocalModel(heavy) = %+v %v, want the nearest listed model", e, ok)
	}
	full, _ := LoadCatalog()
	if e, _ := full.LocalModel(Standard); e.ID != "Qwen3.6-27B" {
		t.Errorf("standard local = %s", e.ID)
	}
}

func TestParseCatalogRejects(t *testing.T) {
	const p = `"provider":"anthropic",`
	for name, in := range map[string]string{
		"bad json":   `[`,
		"bad weight": `[{"id":"a",` + p + `"weight":"huge","tier":"latest"}]`,
		"bad tier":   `[{"id":"a",` + p + `"weight":"heavy","tier":"old"}]`,
		"empty id":   `[{"id":"",` + p + `"weight":"heavy","tier":"latest"}]`,
		"duplicate": `[{"id":"a",` + p + `"weight":"heavy","tier":"latest"},` +
			`{"id":"a",` + p + `"weight":"light","tier":"prior"}]`,
		"two latests": `[{"id":"a",` + p + `"weight":"heavy","tier":"latest"},` +
			`{"id":"b",` + p + `"weight":"heavy","tier":"latest"}]`,
	} {
		if _, err := ParseCatalog([]byte(in)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestModelCheck(t *testing.T) {
	c, _ := LoadCatalog()
	if NewModelCheck(c)("claude-opus-5-5") {
		t.Error("no probe passed, so no model passes")
	}
	failed := ProbeResult{Link: ClaudeCLI, OK: false}
	if NewModelCheck(c, failed)("claude-opus-5-5") {
		t.Error("a failed probe passes nothing")
	}
	ok := ProbeResult{Link: Local, OK: true, Models: map[Weight]string{Standard: "my-local-model"}}
	check := NewModelCheck(c, ok)
	for _, m := range []string{"my-local-model", "claude-opus-5-5", "claude-sonnet-5"} {
		if !check(m) {
			t.Errorf("%s should pass", m)
		}
	}
	for _, m := range []string{"opus", "claude-opus-9", ""} {
		if check(m) {
			t.Errorf("%q should fail", m)
		}
	}
}

func TestHeadroomPercentLeft(t *testing.T) {
	if got := (Headroom{Limit: 200, Remaining: 50}).PercentLeft(); got != 25 {
		t.Errorf("PercentLeft = %v, want 25", got)
	}
	if got := (Headroom{Remaining: 5}).PercentLeft(); got != 0 {
		t.Errorf("unknown limit = %v, want 0", got)
	}
}

func TestUnregisteredOpenAILinksAreUnavailable(t *testing.T) {
	c := New(nil)
	for _, l := range []Link{OpenAISubscription, OpenAIAPI} {
		if _, err := c.Probe(context.Background(), l); !errors.Is(err, ErrUnavailable) {
			t.Errorf("Probe(%s) = %v, want ErrUnavailable", l, err)
		}
		if _, err := c.Start(context.Background(), Session{Link: l}); !errors.Is(err, ErrUnavailable) {
			t.Errorf("Start(%s) = %v, want ErrUnavailable", l, err)
		}
	}
}

func TestClientMissingLink(t *testing.T) {
	c := New(nil)
	if _, err := c.Start(context.Background(), Session{Link: ClaudeCLI}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("missing adapter = %v, want ErrUnavailable", err)
	}
}
