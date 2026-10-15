package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testToken = "t0k"

func newTestServer(t *testing.T) *Server {
	t.Helper()
	return New(testToken, "testdata/groups.json", false)
}

func do(s *Server, method, target, host string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

// stream reads the event stream for 200 milliseconds and returns the recorder.
func stream(s *Server) *httptest.ResponseRecorder {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/events?t="+testToken, nil).WithContext(ctx)
	req.Host = "127.0.0.1:9000"
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func post(s *Server, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Host = "127.0.0.1:9000"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Datastar-Request", "true")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func TestRefusesMissingToken(t *testing.T) {
	rec := do(newTestServer(t), http.MethodGet, "/events", "127.0.0.1:9000")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestRefusesForeignHost(t *testing.T) {
	rec := do(newTestServer(t), http.MethodGet, "/events?t="+testToken, "evil.example:80")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestPageLoadsDatastarAndOpensStream(t *testing.T) {
	rec := do(newTestServer(t), http.MethodGet, "/?t="+testToken, "127.0.0.1:9000")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `src="/assets/datastar.js"`) ||
		!strings.Contains(body, `data-init="@get('/events'+location.search)"`) {
		t.Fatalf("status = %d, body = %q", rec.Code, body)
	}
	if n := strings.Count(body, "<script"); n != 1 {
		t.Errorf("script tags = %d, want only the Datastar one", n)
	}
}

func TestAssetServedWithoutToken(t *testing.T) {
	rec := do(newTestServer(t), http.MethodGet, "/assets/datastar.js", "127.0.0.1:9000")
	if rec.Code != http.StatusOK || rec.Body.Len() < 30000 {
		t.Fatalf("status = %d, size = %d", rec.Code, rec.Body.Len())
	}
}

func TestPageDrawsEveryStageAndAgent(t *testing.T) {
	body := do(newTestServer(t), http.MethodGet, "/?t="+testToken, "127.0.0.1:9000").Body.String()
	for _, want := range append(append([]string{}, Stages...), Agents...) {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if !strings.Contains(body, `data-on:change="@post('/agents/builder/model'+location.search)"`) {
		t.Errorf("builder select does not post its model change: %q", body)
	}
}

func TestPageAndStreamCarryNoToken(t *testing.T) {
	s := New("x'y", "testdata/groups.json", false)
	page := do(s, http.MethodGet, "/?t=x'y", "127.0.0.1:9000").Body.String()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/events?t=x'y", nil).WithContext(ctx)
	req.Host = "127.0.0.1:9000"
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	for name, body := range map[string]string{"page": page, "stream": rec.Body.String()} {
		if strings.Contains(body, "x'y") || strings.Contains(body, "x&#39;y") || strings.Contains(body, `x\u0027y`) {
			t.Errorf("%s embeds the token: %q", name, body)
		}
	}
}

func TestEventsShowReadError(t *testing.T) {
	body := stream(New(testToken, "testdata/missing.json", false)).Body.String()
	if !strings.Contains(body, `data: elements <div id="groups"><p class="Blocked">read groups view:`) {
		t.Fatalf("stream hides the read error: %q", body)
	}
}

func TestEventsPatchTableAndDiagram(t *testing.T) {
	rec := stream(newTestServer(t))
	body := rec.Body.String()
	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("type = %q", rec.Header().Get("Content-Type"))
	}
	if n := strings.Count(body, "event: datastar-patch-elements\n"); n != 2 {
		t.Errorf("patch events = %d, want 2 (table and diagram)", n)
	}
	for _, want := range []string{`data: elements <div id="groups">`, "data: namespace svg",
		`id="ptr-tg-1.2"`, `id="ptr-tg-1.3" class="pointer blocked"`} {
		if !strings.Contains(body, want) {
			t.Errorf("stream missing %q", want)
		}
	}
}

func TestEventsRenderRowsAndResumeOnlyWhenBlocked(t *testing.T) {
	body := stream(newTestServer(t)).Body.String()
	for _, want := range []string{"tg-1.2", "tg-1.3", "claude-opus-5-5", "regression", "You are the builder."} {
		if !strings.Contains(body, want) {
			t.Errorf("stream missing %q", want)
		}
	}
	if n := strings.Count(body, "/resume"); n != 1 {
		t.Errorf("resume buttons = %d, want 1", n)
	}
}

func TestEveryStreamLineIsAField(t *testing.T) {
	for _, line := range strings.Split(stream(newTestServer(t)).Body.String(), "\n") {
		if line != "" && !strings.HasPrefix(line, "event: ") && !strings.HasPrefix(line, "data: ") {
			t.Errorf("line is not an event field: %q", line)
		}
	}
}

func TestPatchSplitsCarriageReturns(t *testing.T) {
	var b strings.Builder
	if err := patch(&b, "<p id=\"x\">a\rdata: signals {}\r\nb</p>", ""); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.FieldsFunc(b.String(), func(r rune) bool { return r == '\n' || r == '\r' }) {
		if !strings.HasPrefix(line, "event: ") && !strings.HasPrefix(line, "data: elements ") {
			t.Errorf("line escaped its data field: %q", line)
		}
	}
}

func TestResumeRecordsAction(t *testing.T) {
	s := newTestServer(t)
	rec := post(s, "/groups/tg-1.3/resume?t="+testToken, "{}")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "resume tg-1.3") ||
		!strings.Contains(rec.Body.String(), `data: elements <p id="log">`) {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if got := s.Actions(); len(got) != 1 || got[0] != "resume tg-1.3" {
		t.Fatalf("actions = %v", got)
	}
}

func TestResumeRefusesGet(t *testing.T) {
	rec := do(newTestServer(t), http.MethodGet, "/groups/tg-1.3/resume?t="+testToken, "127.0.0.1:9000")
	if rec.Code == http.StatusOK {
		t.Fatalf("GET resume answered 200")
	}
}

func TestSetModelRecordsOnlyTheNamedAgent(t *testing.T) {
	s := newTestServer(t)
	rec := post(s, "/agents/builder/model?t="+testToken, `{"builder":"haiku","reviewer":"sonnet"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if got := s.Actions(); len(got) != 1 || got[0] != "model builder=haiku" {
		t.Fatalf("actions = %v", got)
	}
}

func TestSetModelRefusesUnknownModelOrAgent(t *testing.T) {
	s := newTestServer(t)
	if rec := post(s, "/agents/reviewer/model?t="+testToken, `{"reviewer":"gpt-unknown"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown model status = %d, want 400", rec.Code)
	}
	if rec := post(s, "/agents/nobody/model?t="+testToken, `{"nobody":"haiku"}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown agent status = %d, want 404", rec.Code)
	}
	if got := s.Actions(); len(got) != 0 {
		t.Fatalf("actions = %v, want none", got)
	}
}

func TestSetModelRefusesOversizedBody(t *testing.T) {
	s := newTestServer(t)
	body := `{"builder":"haiku","pad":"` + strings.Repeat("a", 1<<20) + `"}`
	if rec := post(s, "/agents/builder/model?t="+testToken, body); rec.Code != http.StatusBadRequest {
		t.Errorf("oversized body status = %d, want 400", rec.Code)
	}
	if got := s.Actions(); len(got) != 0 {
		t.Fatalf("actions = %v, want none", got)
	}
}

func TestUsageHiddenWhenTrackingOff(t *testing.T) {
	body := stream(newTestServer(t)).Body.String()
	if strings.Contains(body, "Tokens") || strings.Contains(body, "48210") || strings.Contains(body, "1.92") {
		t.Fatalf("usage streamed with tracking off: %q", body)
	}
}

func TestUsageShownWhenTrackingOn(t *testing.T) {
	body := stream(New(testToken, "testdata/groups.json", true)).Body.String()
	for _, want := range []string{"Tokens", "USD", "48210", "1.92"} {
		if !strings.Contains(body, want) {
			t.Errorf("stream missing %q", want)
		}
	}
}
