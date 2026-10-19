package llm

import "testing"

const assistantLine = `{"type":"assistant","message":{"id":"msg_1","type":"message","role":"assistant",` +
	`"content":[],"usage":{"input_tokens":2,"cache_creation_input_tokens":25330,` +
	`"cache_read_input_tokens":7,"output_tokens":9}},"session_id":"s"}`

func TestParseClaudeLineReadsTheUsageOfAnAssistantMessage(t *testing.T) {
	ev, _ := parseClaudeLine([]byte(assistantLine), "s")
	if ev.Kind != KindOutput || ev.Msg == nil || ev.Msg.ID != "msg_1" {
		t.Fatalf("got %+v", ev)
	}

	want := Usage{TokensIn: 2, TokensOut: 9, CacheRead: 7, CacheWrite: 25330}
	if ev.Msg.Usage != want {
		t.Fatalf("usage %+v, want %+v", ev.Msg.Usage, want)
	}
}

func TestParseClaudeLineSkipsAnAssistantLineWithoutUsage(t *testing.T) {
	ev, _ := parseClaudeLine([]byte(`{"type":"assistant","message":{"id":"msg_1","content":[]}}`), "s")
	if ev.Msg != nil {
		t.Fatalf("got %+v, want no message usage", ev.Msg)
	}
}

func TestUsageTallyCountsEachMessageOnce(t *testing.T) {
	var tl UsageTally
	if tl.Total() != nil {
		t.Fatal("an empty tally has a total")
	}

	tl.Add(&MsgUsage{ID: "a", Usage: Usage{TokensIn: 2, TokensOut: 3}})
	tl.Add(&MsgUsage{ID: "a", Usage: Usage{TokensIn: 2, TokensOut: 3}})
	tl.Add(&MsgUsage{ID: "b", Usage: Usage{TokensIn: 4, CacheRead: 5, CacheWrite: 6, USD: 0.5}})
	tl.Add(nil)

	got := *tl.Total()
	want := Usage{TokensIn: 6, TokensOut: 3, CacheRead: 5, CacheWrite: 6, USD: 0.5}
	if got != want {
		t.Fatalf("total %+v, want %+v", got, want)
	}
}

func TestUsageTallyKeepsTheLastCountOfARepeatedMessage(t *testing.T) {
	var tl UsageTally
	tl.Add(&MsgUsage{ID: "a", Usage: Usage{TokensOut: 7}})
	tl.Add(&MsgUsage{ID: "a", Usage: Usage{TokensOut: 40}})

	if got := tl.Total().TokensOut; got != 40 {
		t.Fatalf("out %d, want 40", got)
	}
}

func TestUsageTallyCountsMessagesWithoutAnIdSeparately(t *testing.T) {
	var tl UsageTally
	tl.Add(&MsgUsage{Usage: Usage{TokensOut: 1}})
	tl.Add(&MsgUsage{Usage: Usage{TokensOut: 2}})

	if got := tl.Total().TokensOut; got != 3 {
		t.Fatalf("out %d, want 3", got)
	}
}
