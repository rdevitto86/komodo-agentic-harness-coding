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

func TestRefusesMissingToken(t *testing.T) {
	rec := do(newTestServer(t), http.MethodGet, "/groups", "127.0.0.1:9000")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestRefusesForeignHost(t *testing.T) {
	rec := do(newTestServer(t), http.MethodGet, "/groups?t="+testToken, "evil.example:80")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestPageLoadsHtmxAndPolls(t *testing.T) {
	rec := do(newTestServer(t), http.MethodGet, "/?t="+testToken, "127.0.0.1:9000")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `src="/assets/htmx.min.js"`) || !strings.Contains(body, "every 2s") {
		t.Fatalf("status = %d, body = %q", rec.Code, body)
	}
}

func TestAssetServedWithoutToken(t *testing.T) {
	rec := do(newTestServer(t), http.MethodGet, "/assets/htmx.min.js", "127.0.0.1:9000")
	if rec.Code != http.StatusOK || rec.Body.Len() < 40000 {
		t.Fatalf("status = %d, size = %d", rec.Code, rec.Body.Len())
	}
}

func TestGroupsRenderRowsAndResumeOnlyWhenBlocked(t *testing.T) {
	rec := do(newTestServer(t), http.MethodGet, "/groups?t="+testToken, "127.0.0.1:9000")
	body := rec.Body.String()
	for _, want := range []string{"tg-1.2", "tg-1.3", "claude-opus-5-5", "regression", "You are the builder."} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
	if n := strings.Count(body, "/resume"); n != 1 {
		t.Errorf("resume buttons = %d, want 1", n)
	}
}

func TestResumeRecordsAction(t *testing.T) {
	s := newTestServer(t)
	rec := do(s, http.MethodPost, "/groups/tg-1.3/resume?t="+testToken, "127.0.0.1:9000")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "resume tg-1.3") {
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

func TestPageDrawsEveryStageAndAgent(t *testing.T) {
	body := do(newTestServer(t), http.MethodGet, "/?t="+testToken, "127.0.0.1:9000").Body.String()
	for _, want := range append(append([]string{}, Stages...), Agents...) {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if !strings.Contains(body, "/assets/diagram.js") {
		t.Error("page does not load diagram.js")
	}
}

func TestEventsSendsGroupsFrame(t *testing.T) {
	s := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/events?t="+testToken, nil).WithContext(ctx)
	req.Host = "127.0.0.1:9000"
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Header().Get("Content-Type") != "text/event-stream" || strings.Count(body, "data: ") != 1 || !strings.Contains(body, `"group":"tg-1.3"`) {
		t.Fatalf("type = %q, body = %q", rec.Header().Get("Content-Type"), body)
	}
}

func TestSetModelsRecordsOnlyKnownModels(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/agents/model?t="+testToken,
		strings.NewReader("builder=haiku&reviewer=gpt-unknown"))
	req.Host = "127.0.0.1:9000"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.ServeHTTP(httptest.NewRecorder(), req)
	if got := s.Actions(); len(got) != 1 || got[0] != "model builder=haiku" {
		t.Fatalf("actions = %v", got)
	}
}

func TestUsageHiddenWhenTrackingOff(t *testing.T) {
	s := newTestServer(t)
	body := do(s, http.MethodGet, "/groups?t="+testToken, "127.0.0.1:9000").Body.String()
	if strings.Contains(body, "Tokens") || strings.Contains(body, "48210") || strings.Contains(body, "1.92") {
		t.Fatalf("usage shown with tracking off: %q", body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/events?t="+testToken, nil).WithContext(ctx)
	req.Host = "127.0.0.1:9000"
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), `"tokens"`) || strings.Contains(rec.Body.String(), `"usd"`) {
		t.Fatalf("usage streamed with tracking off: %q", rec.Body.String())
	}
}

func TestUsageShownWhenTrackingOn(t *testing.T) {
	s := New(testToken, "testdata/groups.json", true)
	body := do(s, http.MethodGet, "/groups?t="+testToken, "127.0.0.1:9000").Body.String()
	for _, want := range []string{"Tokens", "USD", "48210", "1.92"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}
