package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
)

// checkItems are the acceptance items the run_check tests offer.
func checkItems() []agent.Item {
	return []agent.Item{
		{ID: "acc-1", Text: "the package tests pass", Cmd: "go test -count=1 ./x/ | grep -q PASS"},
		{ID: "acc-2", Text: "the screen looks right"},
	}
}

// schemaOf decodes a tool definition's input schema.
func schemaOf(t *testing.T, d llm.ToolDef) map[string]any {
	t.Helper()

	var m map[string]any
	if err := json.Unmarshal(d.InputSchema, &m); err != nil {
		t.Fatalf("schema of %s: %v", d.Name, err)
	}

	return m
}

// enumOf returns the enum of one property of a schema as text.
func enumOf(t *testing.T, m map[string]any, prop string) string {
	t.Helper()

	p, _ := m["properties"].(map[string]any)[prop].(map[string]any)
	var out []string
	for _, v := range p["enum"].([]any) {
		out = append(out, v.(string))
	}

	return strings.Join(out, ",")
}

func TestRunCheckRunsTheItemsCommandAsWritten(t *testing.T) {
	var got string
	r := newRig(t, func(e *Env) {
		e.Items = checkItems()
		e.Check = func(_ context.Context, command string) (int, string, error) {
			got = command
			return 0, "ok\n", nil
		}
	})

	reply := r.call("run_check", map[string]any{"item": "acc-1"})
	if got != checkItems()[0].Cmd || reply.Text != "ok\n" {
		t.Fatalf("ran %q, reply %q", got, reply.Text)
	}
}

func TestRunCheckReportsAFailingExitStatusWithItsOutput(t *testing.T) {
	r := newRig(t, func(e *Env) {
		e.Items = checkItems()
		e.Check = func(context.Context, string) (int, string, error) { return 3, "bad\n", nil }
	})

	if reply := r.call("run_check", map[string]any{"item": "acc-1"}); reply.Text != "exit status 3\nbad\n" {
		t.Fatalf("reply = %q", reply.Text)
	}
}

func TestRunCheckReportsTheCutOutputOfALongCommand(t *testing.T) {
	r := newRig(t, func(e *Env) {
		e.Items = checkItems()
		e.Check = func(context.Context, string) (int, string, error) {
			return 1, "head...tail", fmt.Errorf("%w: more than 9 bytes; full output in /x.log", agent.ErrOutputOverLimit)
		}
	})

	reply := r.call("run_check", map[string]any{"item": "acc-1"})
	if !strings.HasPrefix(reply.Text, "exit status 1\nhead...tail\n") || !strings.Contains(reply.Text, "/x.log") {
		t.Fatalf("reply = %q", reply.Text)
	}
}

func TestRunCheckRefusesAnUnknownItemAndNamesTheValidOnes(t *testing.T) {
	r := newRig(t, func(e *Env) {
		e.Items = checkItems()
		e.Check = func(context.Context, string) (int, string, error) { return 0, "", errors.New("must not run") }
	})

	reply := r.call("run_check", map[string]any{"item": "acc-9"})
	if !strings.HasPrefix(reply.Text, "error:") || !strings.Contains(reply.Text, `"acc-9"`) ||
		!strings.Contains(reply.Text, "acc-1, acc-2") {
		t.Fatalf("reply = %q", reply.Text)
	}
}

func TestRunCheckRefusesAnItemWithNoCommand(t *testing.T) {
	r := newRig(t, func(e *Env) {
		e.Items = checkItems()
		e.Check = func(context.Context, string) (int, string, error) { return 0, "", errors.New("must not run") }
	})

	reply := r.call("run_check", map[string]any{"item": "acc-2"})
	if !strings.HasPrefix(reply.Text, "error:") || !strings.Contains(reply.Text, "acc-2 has no command") {
		t.Fatalf("reply = %q", reply.Text)
	}
}

func TestRunCheckRefusesCommandTextFromTheModel(t *testing.T) {
	r := newRig(t, func(e *Env) {
		e.Items = checkItems()
		e.Check = func(context.Context, string) (int, string, error) { return 0, "", errors.New("must not run") }
	})

	reply := r.call("run_check", map[string]any{"item": "acc-1", "command": "rm -rf /"})
	if !strings.Contains(reply.Text, "G1") {
		t.Fatalf("reply = %q", reply.Text)
	}
}

func TestRunCheckIsUnavailableWithoutARunner(t *testing.T) {
	r := newRig(t, func(e *Env) { e.Items = checkItems() })

	if reply := r.call("run_check", map[string]any{"item": "acc-1"}); !strings.Contains(reply.Text, "not available") {
		t.Fatalf("reply = %q", reply.Text)
	}
}

func TestRunSchemaListsTheClassesOfTheCallingAgent(t *testing.T) {
	r := newRig(t, nil)

	d, ok := r.b.Definition("run")
	if !ok {
		t.Fatal("run has no definition")
	}
	if got := enumOf(t, schemaOf(t, d), "class"); got != "build,test,deploy" {
		t.Errorf("class enum = %s", got)
	}
	if !strings.Contains(d.Description, "appended") || !strings.Contains(d.Description, "build, test, deploy") {
		t.Errorf("description = %q", d.Description)
	}

	f := newFixture(t)
	g := newTestGuard(t, f, "reviewer", reviewerPerms(), nil, 3)
	b, err := NewRegistry(&Env{Worktree: f.Root}).Bind(reviewerPerms().Tools, g)
	if err != nil {
		t.Fatal(err)
	}

	d, _ = b.Definition("run")
	if got := enumOf(t, schemaOf(t, d), "class"); got != "build,test,lint" {
		t.Errorf("reviewer class enum = %s", got)
	}
}

func TestRunCheckSchemaListsTheGroupsItemIDs(t *testing.T) {
	r := newRig(t, func(e *Env) { e.Items = checkItems() })

	d, ok := r.b.Definition("run_check")
	if !ok {
		t.Fatal("run_check has no definition")
	}

	m := schemaOf(t, d)
	if got := enumOf(t, m, "item"); got != "acc-1,acc-2" {
		t.Errorf("item enum = %s", got)
	}
	if m["additionalProperties"] != false {
		t.Errorf("additionalProperties = %v", m["additionalProperties"])
	}
}

func TestRunCheckSchemaHasNoEnumWithoutItems(t *testing.T) {
	r := newRig(t, nil)

	d, _ := r.b.Definition("run_check")
	p, _ := schemaOf(t, d)["properties"].(map[string]any)["item"].(map[string]any)
	if _, has := p["enum"]; has {
		t.Errorf("item schema = %v, want no enum", p)
	}
}

func TestRunLogsTheCommandItStarted(t *testing.T) {
	var logged []string
	r := newRig(t, func(e *Env) {
		e.Commands["build"] = []string{"printf", "[%s]\n"}
		e.OnRun = func(cmd string) { logged = append(logged, cmd) }
	})

	r.call("run", map[string]any{"class": "build", "args": []string{"a", "b"}})
	r.call("run", map[string]any{"class": "build"})

	want := []string{"printf [%s]\n a b", "printf [%s]\n", "build"}
	if strings.Join(logged, "|") != strings.Join(want, "|") {
		t.Errorf("logged %q, want %q", logged, want)
	}
}
