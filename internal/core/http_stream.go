package core

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// sseLineMax is the longest SSE line the reader accepts.
const sseLineMax = 1 << 20

// SSEEvent is one server-sent event.
type SSEEvent struct {
	Event string
	Data  string
	ID    string
}

// StreamHooks connects a stream to its consumer.
type StreamHooks struct {
	Activity func()               // called whenever bytes arrive, keep-alives included
	Event    func(SSEEvent) error // called per event; an error aborts the stream
	Restart  func()               // called before a retry so the consumer drops partial output
}

// dropError marks a stream that broke mid-answer.
type dropError struct{ err error }

// Error describes the read failure.
func (e *dropError) Error() string { return "stream dropped: " + e.err.Error() }

// Unwrap exposes the read failure.
func (e *dropError) Unwrap() error { return e.err }

// Stream sends req and feeds SSE events to hooks; a drop mid-answer retries once from the start.
func (c *Client) Stream(ctx context.Context, ep Endpoint, req *http.Request, hooks StreamHooks) (*Response, error) {
	for drops := 0; ; drops++ {
		s, err := c.send(ctx, ep, req)
		if err != nil {
			return nil, c.wrapErr(err)
		}
		res := &Response{Status: s.resp.StatusCode, Header: s.resp.Header, Attempts: s.attempts, Headroom: s.headroom}
		err = readSSE(ctx, s.resp.Body, hooks)
		s.resp.Body.Close()
		if err == nil {
			return res, nil
		}
		var drop *dropError
		if !errors.As(err, &drop) {
			return nil, c.wrapErr(err)
		}
		if drops == 1 {
			return nil, c.wrapErr(fmt.Errorf("stream dropped twice: %w", err))
		}
		c.logf("stream from %s dropped mid-answer: %v; retrying from the start", c.target(req), err)
		if hooks.Restart != nil {
			hooks.Restart()
		}
	}
}

// activityReader reports every read that returns bytes.
type activityReader struct {
	r  io.Reader
	fn func()
}

// Read reads from the wrapped reader and signals activity.
func (a *activityReader) Read(p []byte) (int, error) {
	n, err := a.r.Read(p)
	if n > 0 && a.fn != nil {
		a.fn()
	}
	return n, err
}

// readSSE parses events line by line until the body ends; a broken read is a dropError.
func readSSE(ctx context.Context, body io.Reader, hooks StreamHooks) error {
	sc := bufio.NewScanner(&activityReader{r: body, fn: hooks.Activity})
	sc.Buffer(make([]byte, 0, 64<<10), sseLineMax)
	var ev SSEEvent
	var data []string
	pending := false
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if pending {
				ev.Data = strings.Join(data, "\n")
				if hooks.Event != nil {
					if err := hooks.Event(ev); err != nil {
						return err
					}
				}
			}
			ev, data, pending = SSEEvent{}, nil, false
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			ev.Event, pending = value, true
		case "data":
			data, pending = append(data, value), true
		case "id":
			ev.ID, pending = value, true
		}
	}
	err := sc.Err()
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, bufio.ErrTooLong):
		return fmt.Errorf("event stream line exceeds %d bytes", sseLineMax)
	}
	return &dropError{err: err}
}
