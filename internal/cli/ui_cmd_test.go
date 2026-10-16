package cli

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
)

// uiEngine is a scripted engine that counts Ensure calls.
type uiEngine struct {
	scriptEngine
	mu      sync.Mutex
	ensured int
}

// Ensure counts the call.
func (e *uiEngine) Ensure(context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ensured++
	return nil
}

// withUIHooks runs fn once the dashboard listens and then stops it, as Ctrl-C would.
func withUIHooks(t *testing.T, fn func(addr string)) {
	t.Helper()
	oldCtx, oldStarted := uiContext, uiStarted
	t.Cleanup(func() { uiContext, uiStarted = oldCtx, oldStarted })
	ctx, cancel := context.WithCancel(context.Background())
	uiContext = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	uiStarted = func(addr string) {
		go func() {
			defer cancel()
			fn(addr)
		}()
	}
}

// emptyStatus answers every request with an empty status.
func emptyStatus(engine.Request) []engine.Reply {
	return []engine.Reply{okReply(StatusData{Runs: []RunRow{}, Queued: []QueuedRow{}, Blocked: []BlockedRow{}})}
}

var loopbackAddr = regexp.MustCompile(`^127\.0\.0\.1:[0-9]+$`)

func TestUIRunsFromAShellWithNoTerminal(t *testing.T) {
	h := newHarness(t)
	eng := &uiEngine{scriptEngine: scriptEngine{fn: emptyStatus}}
	h.engine = eng
	var page, post int
	var body string
	withUIHooks(t, func(addr string) {
		resp, err := http.Get("http://" + addr + "/")
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			page, body = resp.StatusCode, string(b)
		}
		if resp, err := http.Post("http://"+addr+"/state", "text/plain", strings.NewReader("x")); err == nil {
			_ = resp.Body.Close()
			post = resp.StatusCode
		}
	})
	code, e := h.envelope("ui")
	d := dataOf[UIData](t, e)
	if code != 0 || !loopbackAddr.MatchString(d.Address) {
		t.Fatalf("exit %d, data %+v, error %+v", code, d, e.Error)
	}
	if eng.ensured != 1 {
		t.Fatalf("Ensure called %d times, want 1", eng.ensured)
	}
	if page != 200 || !strings.Contains(body, "Komodo") || post != 405 {
		t.Fatalf("page %d, post %d, body %.80q", page, post, body)
	}
}
