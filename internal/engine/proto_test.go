package engine

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

func TestRequestRoundTrip(t *testing.T) {
	in := Request{
		ID: "0192e4c5-8b10-7c3a-9d52-4e6f1a7b2c80", Verb: VerbRun, Target: "tg-1.2", Root: "/r",
		Flags: Flags{All: true, TimeoutS: 5, Schedule: time.Date(2026, 10, 16, 2, 0, 0, 0, time.UTC)},
		Event: &Event{Type: bus.TypeRunStart, Run: "r1", Data: &bus.RunStart{Target: "tg-1.2", Groups: []string{"tg-1.2"}, Source: "cli"}},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Request
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	if out.ID != in.ID || out.Verb != VerbRun || !out.Flags.Schedule.Equal(in.Flags.Schedule) || !out.Flags.All {
		t.Fatalf("round trip lost fields: %+v", out)
	}
	if rs, ok := out.Event.Data.(*bus.RunStart); !ok || rs.Target != "tg-1.2" {
		t.Fatalf("event data = %#v", out.Event.Data)
	}
}

func TestZeroFlagsEncodeEmpty(t *testing.T) {
	b, err := json.Marshal(Request{ID: "x", Verb: VerbStatus})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"flags":{}`) || strings.Contains(string(b), "schedule") {
		t.Fatalf("zero flags encode as %s", b)
	}
}

func TestReplyRoundTrip(t *testing.T) {
	in := Reply{ID: "x", Status: StatusRefused, Code: CodeNotBlocked, Detail: "d", Data: json.RawMessage(`{"a":1}`), Final: true}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Reply
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Code != "not_blocked" || out.Status != "refused" || !out.Final || string(out.Data) != `{"a":1}` {
		t.Fatalf("reply = %+v", out)
	}
}

func TestConstantsMatchChapter7(t *testing.T) {
	verbs := []string{VerbRun, VerbStop, VerbStatus, VerbAbandon, VerbReplay, VerbPlan, VerbClean}
	if strings.Join(verbs, ",") != "run,stop,status,abandon,replay,plan,clean" {
		t.Fatalf("verbs = %v", verbs)
	}
	codes := []string{CodeBadFrame, CodeUnknownTarget, CodeRunRefused, CodeNotBlocked, CodeEngineStopping}
	if strings.Join(codes, ",") != "bad_frame,unknown_target,run_refused,not_blocked,engine_stopping" {
		t.Fatalf("codes = %v", codes)
	}
	if StatusOK != "ok" || StatusRefused != "refused" || StatusError != "error" || StatusEvent != "event" {
		t.Fatal("statuses differ")
	}
}
