package engine

import (
	"context"
	"fmt"
)

// pendingContinue is a continue input queued for a group and the channel that receives its outcome.
type pendingContinue struct {
	g   *group
	ch  <-chan error
	req string
}

// expectContinue registers interest in the outcome of the continue input with this request, before it is queued.
func (m *Manager) expectContinue(request string) <-chan error {
	ch := make(chan error, 1)
	m.mu.Lock()
	m.conts[request] = ch
	m.mu.Unlock()
	return ch
}

// queueContinue registers interest in a continue's outcome, then puts the input on the group's channel.
func (m *Manager) queueContinue(g *group, request string) pendingContinue {
	p := pendingContinue{g: g, ch: m.expectContinue(request), req: request}
	g.enqueue(Input{Kind: InContinue, Request: request})
	return p
}

// continued reports the outcome of a continue input to the command that waits for it.
func (m *Manager) continued(request string, err error) {
	m.mu.Lock()
	ch := m.conts[request]
	delete(m.conts, request)
	m.mu.Unlock()

	if ch != nil {
		ch <- err
	}
}

// awaitContinues waits until each queued continue is applied or failed and returns the first failure.
func (m *Manager) awaitContinues(ctx context.Context, ps []pendingContinue) error {
	var first error

	for _, p := range ps {
		defer m.forgetContinue(p.req)

		select {
		case err := <-p.ch:
			if err != nil && first == nil {
				first = &codeError{CodeRunRefused, fmt.Sprintf("%s was not continued: %v", p.g.spec.Label, err)}
			}
		case <-p.g.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return first
}

// forgetContinue drops the waiter of a continue input that was settled or whose group ended.
func (m *Manager) forgetContinue(request string) {
	m.mu.Lock()
	delete(m.conts, request)
	m.mu.Unlock()
}
