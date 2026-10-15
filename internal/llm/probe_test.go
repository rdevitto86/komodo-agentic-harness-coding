package llm

import (
	"context"
	"testing"
)

// stubProvider answers Probe with a fixed result.
type stubProvider struct{ res ProbeResult }

func (s stubProvider) Link() Link                                 { return s.res.Link }
func (s stubProvider) Probe(context.Context) (ProbeResult, error) { return s.res, nil }
func (s stubProvider) Profile(context.Context, Weight) (CapacityProfile, error) {
	return CapacityProfile{}, nil
}
func (s stubProvider) Start(context.Context, Session) (Stream, error) { return nil, ErrNotYet }
func (s stubProvider) Resume(context.Context, Session, Input) (Stream, error) {
	return nil, ErrNotYet
}

func TestProbeAllReportsEveryLinkAndFeedsModelCheck(t *testing.T) {
	c := New(map[Link]Provider{
		OpenAIAPI: NewOpenAIAPI(),
		Local:     stubProvider{res: ProbeResult{Link: Local, OK: true, Models: map[Weight]string{Standard: "my-local"}}},
	})
	got := c.ProbeAll(context.Background())
	if len(got) != 2 || got[0].Link != Local || !got[0].OK || got[1].Link != OpenAIAPI || got[1].OK || got[1].Reason == "" {
		t.Fatalf("ProbeAll = %+v", got)
	}
	cat, _ := LoadCatalog()
	ok := NewModelCheck(cat, got...)
	if !ok("my-local") || !ok("claude-opus-5-5") || ok("nope") {
		t.Error("the check built from the probe results is wrong")
	}
}
