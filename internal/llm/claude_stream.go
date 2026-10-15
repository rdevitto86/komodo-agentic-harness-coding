package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// rateLimitHold is the reset distance past which a rate limit ends the session.
const rateLimitHold = 60 * time.Second

// closeGrace is how long Close waits for the program to leave after its input closes.
const closeGrace = 3 * time.Second

// claudeStream is the event stream of one claude program session.
type claudeStream struct {
	p       *proc
	id      string
	now     func() time.Time
	events  chan StreamEvent
	quit    chan struct{}
	pending []StreamEvent

	mu      sync.Mutex
	closed  bool
	done    bool // the result event was delivered
	limited bool // a provider limit was delivered
	once    sync.Once
}

// newClaudeStream starts the reader goroutine over the program's output.
func newClaudeStream(p *proc, id string, now func() time.Time) *claudeStream {
	s := &claudeStream{p: p, id: id, now: now, events: make(chan StreamEvent, 64), quit: make(chan struct{})}
	go s.read()
	return s
}

// read parses output lines into events, then reaps the program when its output ends.
func (s *claudeStream) read() {
	defer close(s.events)
	for {
		line, err := s.p.out.ReadBytes('\n')
		if t := bytes.TrimSpace(line); len(t) > 0 {
			ev, _ := parseClaudeLine(t, s.id)
			select {
			case s.events <- ev:
			case <-s.quit:
			}
		}
		if err != nil {
			break
		}
	}
	s.p.exitEr = s.p.cmd.Wait()
	close(s.p.exited)
}

// exited reports whether the program has been reaped.
func (s *claudeStream) exited() bool {
	select {
	case <-s.p.exited:
		return true
	default:
		return false
	}
}

// firstEvent waits for the first parsed line.
func (s *claudeStream) firstEvent(ctx context.Context) (StreamEvent, error) {
	select {
	case ev, ok := <-s.events:
		if !ok {
			<-s.p.exited
			return StreamEvent{}, fmt.Errorf("claude ended before its first line%s: %w", s.p.stderrTail(), ErrUnavailable)
		}
		return ev, nil
	case <-ctx.Done():
		return StreamEvent{}, ctx.Err()
	}
}

// Next returns the next event; a provider limit ends the session with a *ProviderLimit.
func (s *claudeStream) Next(ctx context.Context) (StreamEvent, error) {
	s.mu.Lock()
	closed, done, limited := s.closed, s.done, s.limited
	s.mu.Unlock()
	switch {
	case done:
		return StreamEvent{}, io.EOF
	case closed || limited:
		return StreamEvent{}, ErrStreamClosed
	}
	if len(s.pending) > 0 {
		ev := s.pending[0]
		s.pending = s.pending[1:]
		return ev, nil
	}
	select {
	case ev, ok := <-s.events:
		if !ok {
			<-s.p.exited
			return StreamEvent{}, fmt.Errorf("%w%s: %w", errNoResult, s.p.stderrTail(), io.ErrUnexpectedEOF)
		}
		if pl := s.limitOf(ev); pl != nil {
			s.mu.Lock()
			s.limited = true
			s.mu.Unlock()
			s.p.kill()
			return StreamEvent{}, pl
		}
		if ev.Kind == KindResult {
			s.mu.Lock()
			s.done = true
			s.mu.Unlock()
			s.p.closeInput()
		}
		return ev, nil
	case <-ctx.Done():
		return StreamEvent{}, ctx.Err()
	}
}

// limitOf decides whether an event ends the session on a provider limit.
func (s *claudeStream) limitOf(ev StreamEvent) *ProviderLimit {
	pl := &ProviderLimit{Link: ClaudeCLI, Handle: &ResumeHandle{Link: ClaudeCLI, Kind: "session_id", Value: s.id}}
	switch ev.Kind {
	case KindRetry:
		if ev.Retry.Category != "billing_error" {
			return nil
		}
		pl.Cap = CapSpend
		return pl
	case KindRateLimit:
		rl := ev.RateLimit
		if rl.IsUsingOverage || strings.HasPrefix(rl.Status, "allowed") || rl.ResetAt.IsZero() || rl.ResetAt.Sub(s.now()) <= rateLimitHold {
			return nil
		}
		pl.ResetAt, pl.Cap = rl.ResetAt, capOf(rl.Type)
		return pl
	}
	return nil
}

// capOf maps the program's rate-limit window name to a cap kind.
func capOf(window string) CapKind {
	switch w := strings.ToLower(window); {
	case strings.Contains(w, "opus"), strings.Contains(w, "sonnet"), strings.Contains(w, "model"):
		return CapModel
	case strings.Contains(w, "seven_day"), strings.Contains(w, "week"):
		return CapWeekly
	case strings.Contains(w, "overage"), strings.Contains(w, "spend"):
		return CapSpend
	}
	return CapWindow
}

// Inject writes one user line to the live session.
func (s *claudeStream) Inject(line string) error {
	if s.isEnded() {
		return ErrStreamClosed
	}
	return s.p.writeUser(line)
}

// isEnded reports whether the session can no longer take input.
func (s *claudeStream) isEnded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed || s.limited || s.exited()
}

// Stop applies one rung of the stop ladder.
func (s *claudeStream) Stop(level StopLevel) error {
	if s.isEnded() {
		return ErrStreamClosed
	}
	if level == Kill {
		s.p.kill()
		return nil
	}
	return signalProc(s.p.cmd.Process, level)
}

// Close closes the program's input, waits briefly for it to leave, then kills what remains.
func (s *claudeStream) Close() error {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		close(s.quit)
		s.p.closeInput()
		select {
		case <-s.p.exited:
		case <-time.After(closeGrace):
			s.p.kill()
			<-s.p.exited
		}
	})
	return nil
}

// claudeHead is the type fields every program line carries.
type claudeHead struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
}

// parseClaudeLine maps one program line to an event; a line that is not JSON is plain output.
func parseClaudeLine(line []byte, sessionID string) (StreamEvent, error) {
	raw := json.RawMessage(append([]byte(nil), line...))
	var head claudeHead
	if err := json.Unmarshal(line, &head); err != nil {
		quoted, _ := json.Marshal(string(line))
		return StreamEvent{Kind: KindOutput, Raw: quoted}, nil
	}
	ev := StreamEvent{Kind: KindOutput, Raw: raw}
	switch {
	case head.Type == "system" && head.Subtype == "init":
		var v struct {
			APIKeySource string `json:"apiKeySource"`
		}
		_ = json.Unmarshal(line, &v)
		ev.Kind, ev.Init = KindInit, &Init{APIKeySource: v.APIKeySource}
	case head.Type == "system" && head.Subtype == "api_retry":
		var v struct {
			Error      string `json:"error"`
			Category   string `json:"category"`
			RetryDelay int    `json:"retry_delay_ms"`
		}
		_ = json.Unmarshal(line, &v)
		cat := v.Error
		if cat == "" {
			cat = v.Category
		}
		ev.Kind, ev.Retry = KindRetry, &Retry{Category: cat, RetryDelayMS: v.RetryDelay}
	case head.Type == "rate_limit_event":
		var v struct {
			Info struct {
				ResetsAt       float64 `json:"resetsAt"`
				Status         string  `json:"status"`
				Type           string  `json:"rateLimitType"`
				IsUsingOverage bool    `json:"isUsingOverage"`
			} `json:"rate_limit_info"`
		}
		_ = json.Unmarshal(line, &v)
		rl := &RateLimit{IsUsingOverage: v.Info.IsUsingOverage, Type: v.Info.Type, Status: v.Info.Status}
		if v.Info.ResetsAt > 0 {
			rl.ResetAt = time.Unix(int64(v.Info.ResetsAt), 0)
		}
		ev.Kind, ev.RateLimit = KindRateLimit, rl
	case head.Type == "result":
		ev.Kind, ev.Result = KindResult, parseClaudeResult(line, sessionID)
	}
	return ev, nil
}

// parseClaudeResult reads the answer, usage and handle out of a result line.
func parseClaudeResult(line []byte, sessionID string) *Result {
	var v struct {
		Structured json.RawMessage `json:"structured_output"`
		Text       string          `json:"result"`
		Cost       float64         `json:"total_cost_usd"`
		Usage      struct {
			In         int `json:"input_tokens"`
			Out        int `json:"output_tokens"`
			CacheRead  int `json:"cache_read_input_tokens"`
			CacheWrite int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(line, &v)
	r := &Result{
		Usage:  &Usage{TokensIn: v.Usage.In, TokensOut: v.Usage.Out, CacheRead: v.Usage.CacheRead, CacheWrite: v.Usage.CacheWrite, USDEquiv: v.Cost},
		Handle: ResumeHandle{Link: ClaudeCLI, Kind: "session_id", Value: sessionID},
	}
	switch {
	case len(v.Structured) > 0 && string(v.Structured) != "null":
		r.Output = v.Structured
	case json.Valid([]byte(v.Text)) && strings.HasPrefix(strings.TrimSpace(v.Text), "{"):
		r.Output = json.RawMessage(v.Text)
	}
	return r
}
