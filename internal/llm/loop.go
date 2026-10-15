package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// Messages the loop sends on its own behalf.
const (
	continueText = "continue"
	finalText    = "The work is complete. Return the final result now as JSON that matches the result schema, and nothing else."
	noToolsText  = "error: tools are off for the final call; return the result as JSON that matches the result schema"
)

// maxContextTokens caps the conversation size before old tool outputs are stubbed.
const maxContextTokens = 200000

// messagesFile is the per-turn conversation record in the session folder.
const messagesFile = "messages.jsonl"

// loopState is the mutable state of one tool loop.
type loopState struct {
	cfg       loopConfig
	now       func() time.Time
	path      string
	file      *os.File
	msgs      []message
	usage     Usage
	turns     int // model calls completed
	lastIn    int // input tokens the provider reported for the last call
	sentLen   int // messages that call carried
	calls     int // tool calls so far, numbering the output stubs
	info      map[string]toolInfo
	wantFinal bool

	mu     sync.Mutex
	inject []string

	interrupt atomic.Bool
	readyOnce sync.Once
	ready     chan error
}

// toolInfo remembers which tool made an output, for its stub.
type toolInfo struct {
	name string
	n    int
}

// newLoop opens messages.jsonl and records the starting history.
func newLoop(cfg loopConfig, resume bool) (*loopState, error) {
	l := &loopState{cfg: cfg, now: cfg.Now, info: map[string]toolInfo{}, ready: make(chan error, 1)}
	if l.now == nil {
		l.now = time.Now
	}
	l.path = filepath.Join(cfg.Session.Dir, messagesFile)
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if resume {
		saved, err := loadMessages(l.path)
		if err != nil {
			return nil, err
		}
		l.msgs = saved
		flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	f, err := os.OpenFile(l.path, flags, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", messagesFile, err)
	}
	l.file = f
	if !resume {
		for _, m := range cfg.Messages {
			if err := l.add(m.Role, m.Content); err != nil {
				_ = f.Close()
				return nil, err
			}
		}
	}
	return l, nil
}

// messageLine is one line of messages.jsonl.
type messageLine struct {
	Turn    int             `json:"turn"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Ts      time.Time       `json:"ts"`
}

// loadMessages reads a saved conversation.
func loadMessages(path string) ([]message, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open saved messages: %w", err)
	}
	defer func() { _ = f.Close() }()
	var out []message
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 1 {
			var ml messageLine
			if jerr := json.Unmarshal(line, &ml); jerr != nil {
				return nil, fmt.Errorf("parse saved message %d: %w", len(out)+1, jerr)
			}
			m := message{Role: ml.Role}
			if jerr := json.Unmarshal(ml.Content, &m.Content); jerr != nil {
				return nil, fmt.Errorf("parse saved message %d: %w", len(out)+1, jerr)
			}
			out = append(out, m)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, fmt.Errorf("read saved messages: %w", err)
		}
	}
}

// add appends one message to the history and to messages.jsonl.
func (l *loopState) add(role string, content []block) error {
	l.msgs = append(l.msgs, message{Role: role, Content: content})
	raw, err := json.Marshal(content)
	if err != nil {
		return fmt.Errorf("encode message: %w", err)
	}
	line, _ := json.Marshal(messageLine{Turn: len(l.msgs), Role: role, Content: raw, Ts: l.now()})
	if _, err := l.file.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write %s: %w", messagesFile, err)
	}
	return nil
}

// addUser appends a user message, carrying any lines injected since the last one.
func (l *loopState) addUser(blocks ...block) error {
	l.mu.Lock()
	for _, line := range l.inject {
		blocks = append(blocks, block{Type: "text", Text: line})
	}
	l.inject = nil
	l.mu.Unlock()
	return l.add("user", blocks)
}

// signalReady reports the first outcome of the session once.
func (l *loopState) signalReady(err error) { l.readyOnce.Do(func() { l.ready <- err }) }

// startLoop runs the tool loop for one session and returns its stream once the first reply has begun.
func startLoop(ctx context.Context, cfg loopConfig) (Stream, error) {
	l, err := newLoop(cfg, false)
	if err != nil {
		return nil, err
	}
	return l.begin(ctx)
}

// resumeLoop loads a saved conversation and continues it with new input.
func resumeLoop(ctx context.Context, cfg loopConfig, text string) (Stream, error) {
	l, err := newLoop(cfg, true)
	if err != nil {
		return nil, err
	}
	if err := l.addUser(block{Type: "text", Text: text}); err != nil {
		return nil, err
	}
	return l.begin(ctx)
}

// begin starts the loop goroutine and waits for the first reply to begin or fail.
func (l *loopState) begin(ctx context.Context) (Stream, error) {
	runCtx, cancel := context.WithCancel(context.Background())
	st := &loopStream{l: l, items: make(chan loopItem, 64), quit: make(chan struct{}), cancel: cancel}
	go func() {
		defer recoverer(l.cfg.Faults).Recover(llmSource, nil)
		res, err := l.run(runCtx, st)
		err = l.mapErr(err)
		l.signalReady(err)
		_ = l.file.Close()
		if err != nil {
			st.push(loopItem{err: err})
		} else {
			st.push(loopItem{ev: StreamEvent{Kind: KindResult, Raw: resultRaw(res), Result: res}})
		}
		close(st.items)
	}()
	select {
	case err := <-l.ready:
		if err != nil {
			cancel()
			return nil, err
		}
		return st, nil
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	}
}

// resultRaw renders the result event as a stream line.
func resultRaw(r *Result) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"type": "result", "structured_output": r.Output, "usage": r.Usage})
	return b
}

// mapErr turns a wire error into the session-level error the Task Manager understands.
func (l *loopState) mapErr(err error) error {
	if err == nil {
		return nil
	}
	var le *core.LimitError
	if errors.As(err, &le) {
		pl := &ProviderLimit{Link: l.cfg.Link}
		switch le.Kind {
		case core.LimitSpend:
			pl.Cap = CapSpend
		case core.LimitQuota:
			pl.Cap = CapQuota
		default:
			pl.Cap = CapWindow
		}
		if le.RetryAfter > 0 {
			pl.ResetAt = l.now().Add(le.RetryAfter)
		}
		if l.turns > 0 {
			pl.Handle = &ResumeHandle{Link: l.cfg.Link, Kind: "messages_file", Value: l.path}
		}
		return pl
	}
	var se *core.StatusError
	if errors.As(err, &se) {
		switch se.Status {
		case 404:
			return fmt.Errorf("%s: %w", se.Error(), ErrModelGone)
		case 401, 403:
			return fmt.Errorf("%s: %w", se.Error(), ErrUnauthorized)
		}
	}
	return err
}

// loopItem is one thing the loop goroutine hands to the stream: an event or the final error.
type loopItem struct {
	ev  StreamEvent
	err error
}

// loopStream is the Stream of a tool-loop session.
type loopStream struct {
	l      *loopState
	items  chan loopItem
	quit   chan struct{}
	cancel context.CancelFunc

	mu     sync.Mutex
	closed bool
	done   bool
	failed bool
}

// push hands an item to the consumer unless the stream was closed.
func (s *loopStream) push(it loopItem) {
	select {
	case s.items <- it:
	case <-s.quit:
	}
}

// Next returns the next event; the result event is followed by io.EOF.
func (s *loopStream) Next(ctx context.Context) (StreamEvent, error) {
	s.mu.Lock()
	closed, done, failed := s.closed, s.done, s.failed
	s.mu.Unlock()
	switch {
	case done:
		return StreamEvent{}, io.EOF
	case closed || failed:
		return StreamEvent{}, ErrStreamClosed
	}
	select {
	case it, ok := <-s.items:
		s.mu.Lock()
		defer s.mu.Unlock()
		switch {
		case !ok:
			s.failed = true
			return StreamEvent{}, ErrStreamClosed
		case it.err != nil:
			s.failed = true
			return StreamEvent{}, it.err
		case it.ev.Kind == KindResult:
			s.done = true
		}
		return it.ev, nil
	case <-ctx.Done():
		return StreamEvent{}, ctx.Err()
	}
}

// ended reports whether the session can no longer be steered.
func (s *loopStream) ended() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed || s.done || s.failed
}

// Inject queues a line for the next message sent to the model.
func (s *loopStream) Inject(line string) error {
	if s.ended() {
		return ErrStreamClosed
	}
	s.l.mu.Lock()
	s.l.inject = append(s.l.inject, line)
	s.l.mu.Unlock()
	return nil
}

// Stop applies one rung of the stop ladder: Interrupt finishes after the current tool call, the others cancel at once.
func (s *loopStream) Stop(level StopLevel) error {
	if s.ended() {
		return ErrStreamClosed
	}
	if level == Interrupt {
		s.l.interrupt.Store(true)
		return nil
	}
	s.cancel()
	return nil
}

// Close cancels the loop; calling it twice is safe.
func (s *loopStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	close(s.quit)
	return nil
}

// emit hands an output event to the consumer.
func (s *loopStream) emit(kind StreamKind, raw any) {
	b, _ := json.Marshal(raw)
	s.push(loopItem{ev: StreamEvent{Kind: kind, Raw: b}})
}

// run is the tool loop: work with tools on auto, then one final call that returns the result schema.
func (l *loopState) run(ctx context.Context, st *loopStream) (*Result, error) {
	var rejected, schemaTries int
	var cut bool
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := l.fitContext(); err != nil {
			return nil, err
		}
		final := l.wantFinal
		res, err := l.cfg.Wire.turn(ctx, turnRequest{Messages: l.msgs, Final: final}, turnHooks{Activity: func() { l.signalReady(nil) }})
		if err != nil {
			return nil, err
		}
		l.account(res)
		if err := l.add("assistant", res.Content); err != nil {
			return nil, err
		}
		st.emit(KindOutput, map[string]any{"type": "assistant", "message": res.Content})
		for i := range res.Headroom {
			h := res.Headroom[i]
			raw, _ := json.Marshal(map[string]any{"type": "headroom", "name": h.Name, "limit": h.Limit, "remaining": h.Remaining, "reset": h.Reset})
			st.push(loopItem{ev: StreamEvent{Kind: KindHeadroom, Raw: raw, Headroom: &h}})
		}
		if res.Stop == stopMaxTokens {
			if cut {
				return nil, &EndError{Reason: state.ReasonNoResult, Detail: "reply cut off twice at the output limit"}
			}
			cut = true
			if err := l.addUser(l.resultsFor(ctx, st, res, !final)...); err != nil {
				return nil, err
			}
			if err := l.appendText(continueText); err != nil {
				return nil, err
			}
			continue
		}
		cut = false
		if res.Stop == stopToolUse {
			if err := l.addUser(l.resultsFor(ctx, st, res, !final)...); err != nil {
				return nil, err
			}
			if final {
				schemaTries++
				if schemaTries > 1 {
					return nil, &EndError{Reason: state.ReasonSchemaFail, Detail: "the final call asked for tools"}
				}
				continue
			}
			if l.interrupt.Load() {
				l.wantFinal = true
				if err := l.appendText(finalText); err != nil {
					return nil, err
				}
			}
			continue
		}
		if !final {
			l.wantFinal = true
			if err := l.addUser(block{Type: "text", Text: finalText}); err != nil {
				return nil, err
			}
			continue
		}
		out, why := l.checkResult(res)
		if why != "" {
			schemaTries++
			if schemaTries > 1 {
				return nil, &EndError{Reason: state.ReasonSchemaFail, Detail: why}
			}
			if err := l.addUser(block{Type: "text", Text: "Your result did not match the result schema: " + why + ". Return the result again as JSON that matches the schema."}); err != nil {
				return nil, err
			}
			continue
		}
		if g := l.cfg.Session.Gate; g != nil {
			fb, err := g.Check(ctx, out)
			if err != nil {
				return nil, fmt.Errorf("check the result: %w", err)
			}
			if fb != "" {
				rejected++
				if rejected >= 2 {
					return nil, &EndError{Reason: state.ReasonCheckFail, Detail: "two results failed the role's check"}
				}
				l.wantFinal, schemaTries = false, 0
				if err := l.addUser(block{Type: "text", Text: fb}); err != nil {
					return nil, err
				}
				continue
			}
		}
		return &Result{Output: out, Usage: &l.usage, Handle: ResumeHandle{Link: l.cfg.Link, Kind: "messages_file", Value: l.path}}, nil
	}
}

// appendText adds a text block to the user message just written, or starts one.
func (l *loopState) appendText(text string) error {
	if n := len(l.msgs); n > 0 && l.msgs[n-1].Role == "user" {
		l.msgs[n-1].Content = append(l.msgs[n-1].Content, block{Type: "text", Text: text})
		raw, _ := json.Marshal(l.msgs[n-1].Content)
		line, _ := json.Marshal(messageLine{Turn: n, Role: "user", Content: raw, Ts: l.now()})
		if _, err := l.file.Write(append(line, '\n')); err != nil {
			return fmt.Errorf("write %s: %w", messagesFile, err)
		}
		return nil
	}
	return l.addUser(block{Type: "text", Text: text})
}

// account adds a turn's usage and remembers the size the provider reported.
func (l *loopState) account(res turnResult) {
	l.turns++
	u := res.Usage
	if l.cfg.Price != nil {
		l.usage.USD += l.cfg.Price(u)
	}
	l.usage.TokensIn += u.TokensIn
	l.usage.TokensOut += u.TokensOut
	l.usage.CacheRead += u.CacheRead
	l.usage.CacheWrite += u.CacheWrite
	l.lastIn = u.TokensIn + u.CacheRead + u.CacheWrite
	l.sentLen = len(l.msgs)
}

// resultsFor runs every tool call of a reply in order and returns the tool_result blocks; with run false it refuses them.
func (l *loopState) resultsFor(ctx context.Context, st *loopStream, res turnResult, run bool) []block {
	var out []block
	for _, b := range res.Content {
		if b.Type != "tool_use" {
			continue
		}
		l.calls++
		text := noToolsText
		if run {
			text = l.dispatch(ctx, b)
		}
		l.info[b.ID] = toolInfo{name: b.Name, n: l.calls}
		out = append(out, block{Type: "tool_result", ToolUseID: b.ID, Content: text})
	}
	if len(out) > 0 {
		st.emit(KindOutput, map[string]any{"type": "user", "message": out})
	}
	return out
}

// dispatch sends one tool call to the tool host.
func (l *loopState) dispatch(ctx context.Context, b block) string {
	host := l.cfg.Session.Tools
	if host == nil {
		return "error: no tools are available"
	}
	return host.Dispatch(ctx, b.Name, b.Input).Text
}

// checkResult validates the final reply against the agent's schema and returns the output or the reason it failed.
func (l *loopState) checkResult(res turnResult) (json.RawMessage, string) {
	var text string
	for _, b := range res.Content {
		if b.Type == "text" {
			text += b.Text
		}
	}
	schema := l.cfg.Session.Schema
	if len(schema) == 0 {
		raw, _ := json.Marshal(text)
		return raw, ""
	}
	if !json.Valid([]byte(text)) {
		return nil, "the reply is not valid JSON"
	}
	if err := validateSchema(schema, json.RawMessage(text)); err != nil {
		return nil, err.Error()
	}
	return json.RawMessage(text), ""
}
