package dashboard

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testToken = "t0k"

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
	for _, want := range append([]string{"Intake", "Build", "Test · review", "Commit", "Cleanup", "Done"}, Agents...) {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if len(Agents) != 4 {
		t.Errorf("agents = %v, want planner, builder, Quality Assurance, reviewer", Agents)
	}
	if !strings.Contains(body, `name="viewport"`) {
		t.Errorf("page has no viewport meta for phone width")
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
	if !strings.Contains(body, `data: elements <div id="groups"><p class="err">read dashboard view:`) {
		t.Fatalf("stream hides the read error: %q", body)
	}
}

func TestEventsPatchCardsAndDiagram(t *testing.T) {
	rec := stream(newTestServer(t))
	body := rec.Body.String()
	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("type = %q", rec.Header().Get("Content-Type"))
	}
	if n := strings.Count(body, "event: datastar-patch-elements\n"); n != 2 {
		t.Errorf("patch events = %d, want 2 (html parts and diagram)", n)
	}
	flat := strings.Join(strings.Fields(strings.ReplaceAll(body, "\ndata: elements ", " ")), " ")
	for _, want := range []string{`<div id="groups" class="cards">`, "data: namespace svg",
		`id="ptr-tg-1.2" class="pointer active"`, `id="ptr-tg-1.3" class="pointer blocked"`,
		`id="ptr-tg-1.4" class="pointer paused"`, `id="ptr-tg-1.0" class="pointer done"`} {
		if !strings.Contains(flat, want) {
			t.Errorf("stream missing %q", want)
		}
	}
}

func TestEventsRenderCardsAndResumeOnlyWhenHeld(t *testing.T) {
	body := stream(newTestServer(t)).Body.String()
	for _, want := range []string{"tg-1.2", "tg-1.3", "Audit log", "opus", "regression: Quality Assurance failed 3 times",
		"You are the builder.", "2 / 5 tasks", "18 min"} {
		if !strings.Contains(body, want) {
			t.Errorf("stream missing %q", want)
		}
	}
	if n := strings.Count(body, "/resume"); n != 2 {
		t.Errorf("resume buttons = %d, want 2 (one blocked, one paused)", n)
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
		!strings.Contains(rec.Body.String(), `data: elements <p id="log"`) {
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
	if rec := post(
		s,
		"/agents/reviewer/model?t="+testToken,
		`{"reviewer":"gpt-unknown"}`,
	); rec.Code != http.StatusBadRequest {
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
	for _, bad := range []string{"Tokens", "USD", "48,210", "393,610", "$3.96"} {
		if strings.Contains(body, bad) {
			t.Fatalf("usage streamed with tracking off: %q in %q", bad, body)
		}
	}
}

func TestUsageShownWhenTrackingOn(t *testing.T) {
	body := stream(New(testToken, "testdata/groups.json", true)).Body.String()
	for _, want := range []string{"Tokens", "USD", "48,210", "393,610", "$3.96", "$0.41"} {
		if !strings.Contains(body, want) {
			t.Errorf("stream missing %q", want)
		}
	}
}

func TestKPICountsRunningBlockedDoneAndUsage(t *testing.T) {
	k := kpis(view(t))
	if k.Running != 2 || k.Blocked != 1 || k.Done != 1 {
		t.Errorf("running/blocked/done = %d/%d/%d, want 2/1/1", k.Running, k.Blocked, k.Done)
	}
	if k.Tokens != 393610 || k.USD < 3.959 || k.USD > 3.961 {
		t.Errorf("tokens = %d, usd = %.2f, want 393610 and 3.96", k.Tokens, k.USD)
	}
}

func TestKPITilesOnTheStream(t *testing.T) {
	body := stream(New(testToken, "testdata/groups.json", true)).Body.String()
	for _, want := range []string{
		`<div class="kpi running"><b>2</b>`,
		`<div class="kpi blocked"><b>1</b>`,
		`<div class="kpi done"><b>1</b>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("stream missing %q", want)
		}
	}
}

func TestStageCountsFollowGroups(t *testing.T) {
	got := stageCounts(view(t).Groups)
	want := []int{1, 2, 1, 0, 0, 1}
	if len(got) != len(Stages) {
		t.Fatalf("counts = %v, want one per stage %v", got, Stages)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s count = %d, want %d", Stages[i], got[i], want[i])
		}
	}
}

func TestStageBoxesShowLiveCounts(t *testing.T) {
	body := stream(newTestServer(t)).Body.String()
	if !strings.Contains(body, `id="count-build">2<`) || !strings.Contains(body, `id="count-commit">0<`) {
		t.Errorf("stage boxes lack live counts: %q", body)
	}
}

func TestRecentEventsNewestFirstAndCapped(t *testing.T) {
	var es []Event
	base := time.Date(2026, 10, 14, 15, 0, 0, 0, time.UTC)
	for i := range 20 {
		es = append(es, Event{
			Type:    "run-status",
			Subtype: fmt.Sprint("e", i),
			At:      base.Add(time.Duration(i%7) * time.Minute),
		})
	}
	got := recentEvents(es)
	if len(got) != 15 {
		t.Fatalf("events = %d, want 15", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].At.After(got[i-1].At) {
			t.Fatalf("event %d is newer than event %d", i, i-1)
		}
	}
}

func TestEventFeedOrderOnTheStream(t *testing.T) {
	body := stream(newTestServer(t)).Body.String()
	last := -1
	for _, sub := range []string{
		"paused<",
		"resource-limit<",
		"window-warn<",
		"stage-entered<",
		"qa-fail<",
		"group-done<",
	} {
		i := strings.Index(body[strings.Index(body, `id="events"`):], sub)
		if i < 0 || i < last {
			t.Fatalf("event %q at %d, after previous at %d: want newest first", sub, i, last)
		}
		last = i
	}
}

func TestCardShowsChipsProgressAndSessions(t *testing.T) {
	body := stream(newTestServer(t)).Body.String()
	for _, want := range []string{
		`class="chip blocked">Blocked`, `class="chip paused">Paused`, `class="chip done">Done`, `class="chip active">Build`,
		`class="chip active">Test · review`, `style="width:40%"`,
		`class="sessions test-review"`, `class="sess running"`, `class="sess pass"`, `class="sess fail"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("stream missing %q", want)
		}
	}
}

func TestBriefDetailsAreSkippedByMorph(t *testing.T) {
	body := stream(newTestServer(t)).Body.String()
	if n := strings.Count(body, `<details id="brief-tg-1-2-`); n != 1 {
		t.Errorf("details with a group id = %d, want 1", n)
	}
	if n, want := strings.Count(body, " data-ignore-morph>"), len(view(t).Groups); n != want {
		t.Errorf("details that morph skips = %d, want %d", n, want)
	}
}

func TestBriefDetailsIDChangesWithTheBrief(t *testing.T) {
	cs := cards([]Group{{Group: "tg-1", Brief: "a"}, {Group: "tg-1", Brief: "a"}, {Group: "tg-1", Brief: "b"}})
	if cs[0].ID != cs[1].ID || cs[0].ID == cs[2].ID || !strings.HasPrefix(cs[0].ID, "brief-tg-1-") {
		t.Errorf("ids = %q, %q, %q: want a stable id per brief", cs[0].ID, cs[1].ID, cs[2].ID)
	}
}

func TestPointersStackInTheirStageColumn(t *testing.T) {
	ps := pointers([]Group{{Group: "a", Stage: "build"}, {Group: "b", Stage: "intake"}, {Group: "c", Stage: "build"}})
	if ps[0].X != ps[2].X || ps[2].Y != ps[0].Y+26 || ps[1].Y != ps[0].Y {
		t.Errorf("pointers = %+v, want a and c stacked 26 apart in one column, b in the first row of its own", ps)
	}
}

func TestResourceMetersShowEveryLink(t *testing.T) {
	body := stream(newTestServer(t)).Body.String()
	for _, want := range []string{"claude-max", "96%", "codex-api", "41%", `meter hot`} {
		if !strings.Contains(body, want) {
			t.Errorf("stream missing %q", want)
		}
	}
}

func TestRunHeaderShowsRunRepoAndEpic(t *testing.T) {
	body := stream(newTestServer(t)).Body.String()
	for _, want := range []string{"r-7f2", "komodo-auth-api", "Lease renewal and token refresh"} {
		if !strings.Contains(body, want) {
			t.Errorf("stream missing %q", want)
		}
	}
}

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

// view loads the mock view the tests share.
func view(t *testing.T) View {
	t.Helper()
	v, err := New(testToken, "testdata/groups.json", true).readView()
	if err != nil {
		t.Fatal(err)
	}
	return v
}
