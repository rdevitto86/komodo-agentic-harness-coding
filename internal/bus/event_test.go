package bus

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const sessionEndLine = `{
  "type": "run-status",
  "subtype": "session.end",
  "run": "0192e4c5-6f00-7a21-8c3b-2d9e0f1a4b57",
  "group": "0192e4c5-7a10-7d3e-9b2f-6c1d8e4a5f30",
  "time": "2026-10-14T15:31:12Z",
  "data": {
    "stage": "build", "session": "0192e4c6-1d40-7b85-a2f3-8c6e1d0b9a47", "reason": "schema_fail", "agent": "builder",
    "task": "tsk-add-lock", "link": "claude-cli", "tokens": 0, "wall_s": 1140
  }
}`

const runStartLine = `{
  "type": "run-start",
  "subtype": "",
  "run": "0192e4c5-6f00-7a21-8c3b-2d9e0f1a4b57",
  "group": "",
  "time": "2026-10-14T15:20:00Z",
  "data": {"target": "tg-1.2", "groups": ["tg-1.2"], "source": "cli", "schedule": "2026-10-16T02:00:00-07:00"}
}`

// Decode of the example session.end line returns the registered SessionEnd struct.
func TestDecodeSessionEndExample(t *testing.T) {
	var e Event
	if err := json.Unmarshal([]byte(sessionEndLine), &e); err != nil {
		t.Fatal(err)
	}
	se, ok := e.Data.(*SessionEnd)
	if !ok {
		t.Fatalf("data is %T, want *SessionEnd", e.Data)
	}
	if e.Type != TypeRunStatus || e.Subtype != "session.end" || se.Agent != "builder" || se.Reason != "schema_fail" ||
		se.WallS != 1140 || se.Stage != "build" {
		t.Fatalf("decoded wrong: %+v %+v", e, se)
	}
	if want := time.Date(2026, 10, 14, 15, 31, 12, 0, time.UTC); !e.Time.Equal(want) {
		t.Fatalf("time %v, want %v", e.Time, want)
	}
}

// An unknown field in data, or in the envelope, is refused.
func TestDecodeRefusesUnknownField(t *testing.T) {
	inData := strings.Replace(sessionEndLine, `"tokens": 0`, `"tokens": 0, "bogus": 1`, 1)
	inEnvelope := strings.Replace(sessionEndLine, `"subtype"`, `"sequence": 4, "subtype"`, 1)
	for name, line := range map[string]string{"data": inData, "envelope": inEnvelope} {
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err == nil {
			t.Errorf("unknown field in %s accepted", name)
		}
	}
}

// A pair the registry does not know fails decode with ErrUnknownEvent.
func TestDecodeUnregisteredPair(t *testing.T) {
	line := strings.Replace(sessionEndLine, `"session.end"`, `"session.nope"`, 1)
	var e Event
	if err := json.Unmarshal([]byte(line), &e); !errors.Is(err, ErrUnknownEvent) {
		t.Fatalf("err %v, want ErrUnknownEvent", err)
	}
}

// A line with no data body, or a null one, is refused.
func TestDecodeMissingData(t *testing.T) {
	for _, line := range []string{
		`{"type":"run-stop","subtype":"","run":"","group":"","time":"2026-10-14T15:20:00Z"}`,
		`{"type":"run-stop","subtype":"","run":"","group":"","time":"2026-10-14T15:20:00Z","data":null}`,
	} {
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err == nil {
			t.Errorf("accepted %s", line)
		}
	}
}

// The scheduled run-start example decodes with groups, source and schedule intact.
func TestDecodeRunStartSchedule(t *testing.T) {
	var e Event
	if err := json.Unmarshal([]byte(runStartLine), &e); err != nil {
		t.Fatal(err)
	}
	rs := e.Data.(*RunStart)
	if rs.Source != "cli" || len(rs.Groups) != 1 || rs.Groups[0] != "tg-1.2" || rs.Schedule.IsZero() {
		t.Fatalf("decoded wrong: %+v", rs)
	}
	if _, off := rs.Schedule.Zone(); off != -7*3600 {
		t.Fatalf("schedule offset %d, want -25200", off)
	}
}

// An absent schedule stays absent on encode.
func TestEncodeOmitsZeroSchedule(t *testing.T) {
	b, err := json.Marshal(Event{Type: TypeRunStart, Data: &RunStart{Target: "all"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "schedule") {
		t.Fatalf("zero schedule encoded: %s", b)
	}
}

// Every registered pair survives encode then decode unchanged.
func TestRoundTripEveryRegisteredPair(t *testing.T) {
	for _, k := range registeredKeys() {
		newData, _ := lookup(k)
		in := Event{
			Type:    k.Type,
			Subtype: k.Subtype,
			Run:     "r",
			Group:   "g",
			Time:    time.Date(2026, 10, 14, 1, 2, 3, 0, time.UTC),
			Data:    newData(),
		}
		b, err := json.Marshal(in)
		if err != nil {
			t.Fatalf("%v: %v", k, err)
		}
		var out Event
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("%v: %v in %s", k, err, b)
		}
		b2, _ := json.Marshal(out)
		if string(b) != string(b2) {
			t.Errorf("%v: round trip changed\n%s\n%s", k, b, b2)
		}
	}
}

// Ten events written as lines, with the last cut short, decode as nine good lines and one refused torn line.
func TestTornLastLineIsRefused(t *testing.T) {
	var lines []string
	for i := 0; i < 10; i++ {
		b, err := json.Marshal(Event{Type: TypeRunStop, Time: time.Unix(int64(i), 0).UTC(), Data: &RunStop{Target: "ep-1"}})
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(b))
	}
	lines[9] = lines[9][:len(lines[9])/2]
	good, bad := 0, 0
	for _, l := range lines {
		var e Event
		if json.Unmarshal([]byte(l), &e) == nil {
			good++
		} else {
			bad++
		}
	}
	if good != 9 || bad != 1 {
		t.Fatalf("good %d bad %d, want 9 and 1", good, bad)
	}
}
