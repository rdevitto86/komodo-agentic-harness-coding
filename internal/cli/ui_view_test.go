package cli

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestUIRunView(t *testing.T) {
	t.Parallel()

	t.Run("a plan run carries its plan id, start time and groups by label", func(t *testing.T) {
		t.Parallel()

		ts, _ := uiFixture(t, newUIFake(), realAssets(t))
		a := getState(t, ts).Runs[0]

		if a.Plan != "plan-7f3a9c" || a.Target != "plan-7f3a9c" || a.RunID != uiRunA {
			t.Fatalf("run ids = plan %q target %q run %q", a.Plan, a.Target, a.RunID)
		}

		if a.Groups[0].Group != "tg-1.1" || a.Groups[1].Group != "tg-1.2" {
			t.Fatalf("groups = %+v", a.Groups)
		}

		if want := time.Date(2026, 10, 15, 12, 0, 1, 0, time.UTC); !a.Started.Equal(want) {
			t.Fatalf("started = %v, want the earliest event %v", a.Started, want)
		}
	})

	t.Run("a blocked group carries its reason and question", func(t *testing.T) {
		t.Parallel()

		ts, _ := uiFixture(t, newUIFake(), realAssets(t))
		g := getState(t, ts).Runs[0].Groups[1]

		if g.State != "blocked" || g.Reason != "risky_change" || !strings.Contains(g.Question, "lease table") {
			t.Fatalf("blocked group = %+v", g)
		}
	})

	t.Run("a run with no plan id has an empty plan", func(t *testing.T) {
		t.Parallel()

		ts, _ := uiFixture(t, newUIFake(), realAssets(t))

		if b := getState(t, ts).Runs[1]; b.Plan != "" || b.Target != "tg-2.1" {
			t.Fatalf("run B = %+v", b)
		}
	})

	t.Run("each run holds its own events newest first", func(t *testing.T) {
		t.Parallel()

		ts, _ := uiFixture(t, newUIFake(), realAssets(t))
		st := getState(t, ts)

		if ev := st.Runs[0].Events; len(ev) != 2 || ev[0].Group != "tg-1.1" || ev[1].Group != "" {
			t.Fatalf("run A events = %+v", ev)
		}

		if ev := st.Runs[1].Events; len(ev) != 1 || ev[0].Run != uiRunB {
			t.Fatalf("run B events = %+v", ev)
		}
	})

	t.Run("an empty engine gives empty lists, not null", func(t *testing.T) {
		t.Parallel()

		fake := newUIFake()
		fake.empty = true
		ts, _ := uiFixture(t, fake, realAssets(t))

		resp, err := http.Get(ts.URL + "/state")
		if err != nil {
			t.Fatal(err)
		}

		defer func() { _ = resp.Body.Close() }()

		b, _ := io.ReadAll(resp.Body)
		for _, want := range []string{`"runs":[]`, `"queued":[]`, `"events":[]`} {
			if !strings.Contains(string(b), want) {
				t.Errorf("state %s lacks %s", b, want)
			}
		}

		if strings.Contains(string(b), `"error"`) {
			t.Errorf("an empty engine is not an error: %s", b)
		}
	})

	t.Run("a stopped engine shows as an error with no runs", func(t *testing.T) {
		t.Parallel()

		fake := newUIFake()
		fake.down = errUINoEngine
		ts, _ := uiFixture(t, fake, realAssets(t))

		if st := getState(t, ts); st.Error != errUINoEngine.Error() || len(st.Runs) != 0 {
			t.Fatalf("state = %+v", st)
		}
	})
}

func TestUIPlanID(t *testing.T) {
	t.Parallel()

	for target, want := range map[string]string{
		"plan-7f3a9c":  "plan-7f3a9c",
		"plan-7F3A9C":  "",
		"plan-7f3a9":   "",
		"plan-7f3a9cd": "",
		"tg-1.1":       "",
		"":             "",
	} {
		if got := uiPlan(target); got != want {
			t.Errorf("uiPlan(%q) = %q, want %q", target, got, want)
		}
	}
}

func TestUIPageFiles(t *testing.T) {
	t.Parallel()

	ts, _ := uiFixture(t, newUIFake(), realAssets(t))

	for path, kind := range map[string]string{
		"/":       "text/html",
		"/ui.js":  "text/javascript",
		"/ui.css": "text/css",
	} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}

		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), kind) {
			t.Errorf("GET %s = %d %q, want 200 %s", path, resp.StatusCode, resp.Header.Get("Content-Type"), kind)
		}
	}
}
