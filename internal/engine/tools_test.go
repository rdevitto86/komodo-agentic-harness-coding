package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
)

// fakeHub is one running session "s-1" whose single tool echoes its arguments.
type fakeHub struct{}

// Definitions describes the echo tool of session s-1.
func (fakeHub) Definitions(session string) ([]llm.ToolDef, bool) {
	if session != "s-1" {
		return nil, false
	}
	return []llm.ToolDef{{Name: "echo", Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`)}}, true
}

// Call answers with the tool name and its arguments.
func (fakeHub) Call(_ context.Context, session, name string, args json.RawMessage) (string, bool) {
	if session != "s-1" {
		return "", false
	}
	return name + ":" + string(args), true
}

func TestToolVerbsReachTheHubOfTheSession(t *testing.T) {
	x := newEng(t, nil, func(r *rig) { r.d.Tools = fakeHub{} })
	x.serve()

	reps := x.ask(Request{ID: "t1", Verb: VerbToolList, Tool: &ToolRequest{Session: "s-1"}})
	var list ToolList
	if r := reps[len(reps)-1]; r.Status != StatusOK || json.Unmarshal(r.Data, &list) != nil || len(list.Tools) != 1 ||
		list.Tools[0].Name != "echo" {
		t.Fatalf("tool_list = %+v", reps)
	}

	reps = x.ask(Request{
		ID:   "t2",
		Verb: VerbToolCall,
		Tool: &ToolRequest{Session: "s-1", Name: "echo", Args: json.RawMessage(`{"q":1}`)},
	})

	var ans ToolAnswer
	if r := reps[len(reps)-1]; r.Status != StatusOK || json.Unmarshal(r.Data, &ans) != nil || ans.Text != `echo:{"q":1}` {
		t.Fatalf("tool_call = %+v", reps)
	}
}

func TestToolVerbsRefuseAnUnknownSessionAndABadFrame(t *testing.T) {
	x := newEng(t, nil, func(r *rig) { r.d.Tools = fakeHub{} })
	x.serve()

	for name, req := range map[string]Request{
		"list of an ended session": {ID: "a", Verb: VerbToolList, Tool: &ToolRequest{Session: "gone"}},
		"call of an ended session": {ID: "b", Verb: VerbToolCall, Tool: &ToolRequest{Session: "gone", Name: "echo"}},
	} {
		reps := x.ask(req)
		if r := reps[len(reps)-1]; r.Status != StatusRefused || r.Code != CodeUnknownSession {
			t.Errorf("%s = %+v", name, r)
		}
	}

	for name, req := range map[string]Request{
		"list with no session": {ID: "c", Verb: VerbToolList},
		"call with no name":    {ID: "d", Verb: VerbToolCall, Tool: &ToolRequest{Session: "s-1"}},
	} {
		reps := x.ask(req)
		if r := reps[len(reps)-1]; r.Status != StatusError || r.Code != CodeBadFrame {
			t.Errorf("%s = %+v", name, r)
		}
	}
}

func TestToolVerbsOnAnEngineWithNoHubRefuseEverySession(t *testing.T) {
	x := newEng(t, nil)
	x.serve()

	reps := x.ask(Request{ID: "e", Verb: VerbToolList, Tool: &ToolRequest{Session: "s-1"}})
	if r := reps[len(reps)-1]; r.Code != CodeUnknownSession {
		t.Fatalf("reply = %+v", r)
	}
}
