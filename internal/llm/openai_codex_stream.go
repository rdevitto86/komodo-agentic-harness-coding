package llm

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// codexThreadFile is the file in the session folder that holds the thread identifier the program chose.
const codexThreadFile = "codex-thread.txt"

// errCodexNoInject is what Inject returns: a codex exec run takes its whole prompt at launch and reads nothing after.
var errCodexNoInject = errors.New("the codex program takes no input after launch")

// codexStream is the event stream of one codex exec session; it differs from the claude stream only in Inject.
type codexStream struct{ *claudeStream }

// Inject returns an error: codex exec has no way to take a line while it runs.
func (s codexStream) Inject(string) error {
	if s.isEnded() {
		return ErrStreamClosed
	}
	return errCodexNoInject
}

// codexLines follows the JSON lines of one codex exec session: its thread, last answer and failure.
type codexLines struct {
	dir string // the session folder, where the thread identifier is saved

	mu     sync.Mutex
	thread string
	answer string
}

// codexLine holds the fields of a program line that the adapter reads.
type codexLine struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Message  string `json:"message"`
	Error    struct {
		Message string `json:"message"`
	} `json:"error"`
	Item struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"item"`
	Usage struct {
		In     int `json:"input_tokens"`
		Cached int `json:"cached_input_tokens"`
		Out    int `json:"output_tokens"`
	} `json:"usage"`
	// the result lines this adapter writes itself
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
}

// parse maps one program line to an event; a line that is not JSON is plain output.
func (c *codexLines) parse(line []byte) StreamEvent {
	var l codexLine
	if err := json.Unmarshal(line, &l); err != nil {
		quoted, _ := json.Marshal(string(line))
		return StreamEvent{Kind: KindOutput, Raw: quoted}
	}

	ev := StreamEvent{Kind: KindOutput, Raw: json.RawMessage(append([]byte(nil), line...))}

	switch l.Type {
	case "thread.started":
		c.setThread(l.ThreadID)
	case "item.completed":
		if l.Item.Type == "agent_message" {
			c.mu.Lock()
			c.answer = l.Item.Text
			c.mu.Unlock()
		}
	case "turn.completed":
		ev.Kind, ev.Result, ev.Raw = KindResult, c.result(l), c.resultLine(l, false, "")
	case "turn.failed":
		why := l.Error.Message
		ev.Kind, ev.Result, ev.Raw = KindResult, c.result(l), c.resultLine(l, true, why)
	}

	return ev
}

// setThread records the thread identifier and saves it where a resume finds it.
func (c *codexLines) setThread(id string) {
	if id == "" {
		return
	}

	c.mu.Lock()
	c.thread = id
	c.mu.Unlock()

	_ = os.WriteFile(filepath.Join(c.dir, codexThreadFile), []byte(id+"\n"), 0o644)
}

// result builds the result a finished turn ends the session with; the answer is its output when it is a JSON object.
func (c *codexLines) result(l codexLine) *Result {
	c.mu.Lock()
	answer, thread := c.answer, c.thread
	c.mu.Unlock()

	// the program counts cached tokens inside input_tokens
	in, cached := l.Usage.In, l.Usage.Cached
	if cached > 0 && cached <= in {
		in -= cached
	} else {
		cached = 0
	}

	r := &Result{
		Usage:  &Usage{TokensIn: in, TokensOut: l.Usage.Out, CacheRead: cached},
		Handle: ResumeHandle{Link: OpenAISubscription, Kind: "session_id", Value: thread},
	}
	if t := strings.TrimSpace(answer); strings.HasPrefix(t, "{") && json.Valid([]byte(t)) {
		r.Output = json.RawMessage(t)
	}

	return r
}

// resultLine renders the result event as a stream line in the shape the claude stream's checks read.
func (c *codexLines) resultLine(l codexLine, failed bool, why string) json.RawMessage {
	c.mu.Lock()
	answer := c.answer
	c.mu.Unlock()

	v := map[string]any{"type": "result", "subtype": "success", "is_error": failed, "result": answer}
	if failed {
		v["subtype"], v["result"] = "error_turn_failed", why
	} else if t := strings.TrimSpace(answer); strings.HasPrefix(t, "{") && json.Valid([]byte(t)) {
		v["structured_output"] = json.RawMessage(t)
	}

	v["usage"] = map[string]int{
		"input_tokens": l.Usage.In, "cached_input_tokens": l.Usage.Cached, "output_tokens": l.Usage.Out,
	}

	b, _ := json.Marshal(v)
	return b
}

// limit decides whether an error line or a failed turn ends the session on a provider limit.
func (c *codexLines) limit(ev StreamEvent) *ProviderLimit {
	var l codexLine
	if json.Unmarshal(ev.Raw, &l) != nil {
		return nil
	}

	var msg string

	switch {
	case l.Type == "error":
		msg = l.Message
	case l.Type == "result" && l.IsError:
		msg = l.Result
	default:
		return nil
	}

	kind, ok := codexCapOf(msg)
	if !ok {
		return nil
	}

	c.mu.Lock()
	thread := c.thread
	c.mu.Unlock()

	pl := &ProviderLimit{Link: OpenAISubscription, Cap: kind}
	if thread != "" {
		pl.Handle = &ResumeHandle{Link: OpenAISubscription, Kind: "session_id", Value: thread}
	}

	return pl
}

// codexCapOf maps the program's limit message to a cap kind; the reset time in the text is local time and is not read.
func codexCapOf(msg string) (CapKind, bool) {
	m := strings.ToLower(msg)

	switch {
	case strings.Contains(m, "spend cap"), strings.Contains(m, "out of credits"):
		return CapSpend, true
	case strings.Contains(m, "hit your usage limit"):
		return CapWindow, true
	case strings.Contains(m, "quota exceeded"):
		return CapQuota, true
	}

	return "", false
}

// newCodexStream starts the reader goroutine over the program's output.
func newCodexStream(
	p *proc,
	id string,
	dir string,
	now func() time.Time,
	rec Recoverer,
	schema json.RawMessage,
) (codexStream, *codexLines) {
	lines := &codexLines{dir: dir}
	s := startProcStream(&claudeStream{}, p, id, now, rec, schema, lines.parse, lines.limit)
	return codexStream{s}, lines
}
