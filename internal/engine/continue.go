package engine

import (
	"context"
	"errors"
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
	return m.queueInput(g, Input{Kind: InContinue, Request: request})
}

// queueInput registers interest in the outcome of an input that carries a request, then queues it.
func (m *Manager) queueInput(g *group, in Input) pendingContinue {
	p := pendingContinue{g: g, ch: m.expectContinue(in.Request), req: in.Request}
	g.enqueue(in)
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

		var err error

		select {
		case err = <-p.ch:
		case <-p.g.done:
			select {
			case err = <-p.ch:
			default:
				// The group ended before it took the command: a person is never told it worked.
				p.g.mu.Lock()
				st := p.g.snap
				p.g.mu.Unlock()
				err = endedError(p.g.spec.Label, st)
			}
		case <-ctx.Done():
			return ctx.Err()
		}

		if err != nil && first == nil {
			first = refusalOf(p.g.spec.Label, err)
		}
	}

	return first
}

// refusalOf keeps a coded error and turns any other outcome into a refused command.
func refusalOf(label string, err error) error {
	var ce *codeError
	if errors.As(err, &ce) {
		return ce
	}

	return &codeError{CodeRunRefused, fmt.Sprintf("%s was not continued: %v", label, err)}
}

// endedError is the refusal for a command that reached a group that already ended: failed groups name the failure.
func endedError(label string, st State) error {
	if st.Stage == StageDone && st.Outcome == "failed" {
		return &codeError{CodeGroupFailed, label + " failed for good: plan it again to run it fresh"}
	}

	return errors.New("the group ended before it took the command")
}

// forgetContinue drops the waiter of a continue input that was settled or whose group ended.
func (m *Manager) forgetContinue(request string) {
	m.mu.Lock()
	delete(m.conts, request)
	m.mu.Unlock()
}
