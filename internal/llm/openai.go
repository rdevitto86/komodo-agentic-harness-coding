package llm

import (
	"context"
	"fmt"
)

// openAIStub is the place for an OpenAI link; every call returns ErrNotYet.
type openAIStub struct{ link Link }

// Link returns the link the stub names.
func (o openAIStub) Link() Link { return o.link }

// Probe returns ErrNotYet.
func (o openAIStub) Probe(context.Context) (ProbeResult, error) {
	return ProbeResult{Link: o.link, Reason: ErrNotYet.Error()}, o.notYet()
}

// Profile returns ErrNotYet.
func (o openAIStub) Profile(context.Context, Weight) (CapacityProfile, error) {
	return CapacityProfile{}, o.notYet()
}

// Start returns ErrNotYet.
func (o openAIStub) Start(context.Context, Session) (Stream, error) { return nil, o.notYet() }

// Resume returns ErrNotYet.
func (o openAIStub) Resume(context.Context, Session, Input) (Stream, error) {
	return nil, o.notYet()
}

// notYet is the error every call of the stub returns.
func (o openAIStub) notYet() error { return fmt.Errorf("%s: %w", o.link, ErrNotYet) }

// NewOpenAISubscription returns the place for OpenAI's subscription link.
func NewOpenAISubscription() Provider { return openAIStub{link: OpenAISubscription} }

// NewOpenAIAPI returns the place for OpenAI's paid interface link.
func NewOpenAIAPI() Provider { return openAIStub{link: OpenAIAPI} }
