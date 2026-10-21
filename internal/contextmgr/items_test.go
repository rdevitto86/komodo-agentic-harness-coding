package contextmgr

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// setEvent wraps a skill / set body in an event.
func setEvent(d SkillSet) bus.Event {
	return bus.Event{Type: bus.TypeSkill, Subtype: "set", Run: "r", Group: "g", Data: &d}
}

// refusals runs apply for ev on a manager with a real bus and returns the context-refused events published.
func refusals(t *testing.T, ev bus.Event) (*Manager, []ContextRefused) {
	t.Helper()
	RegisterOnce()
	b := bus.New(bus.Options{})
	sub, err := b.Subscribe("test", bus.TypeError)
	if err != nil {
		t.Fatal(err)
	}
	m := newManager(t, func(d *Deps) { d.Bus = b })
	m.apply(ev)
	var out []ContextRefused
	for {
		select {
		case e := <-sub.Events:
			if r, ok := e.Data.(*ContextRefused); ok {
				out = append(out, *r)
			}
		default:
			return m, out
		}
	}
}

func TestSetRefusals(t *testing.T) {
	cases := map[string]struct {
		set   SkillSet
		field string
	}{
		"unknown name": {SkillSet{Name: "standards-none", Enabled: true, Allow: workers}, "name"},
		"bad agent":    {SkillSet{Name: "standards-go", Enabled: true, Allow: []string{"builder", "janitor"}}, "allow"},
		"always out": {
			SkillSet{Name: "standards-go", Enabled: true, Allow: []string{"builder"}, Always: []string{"qa"}},
			"always",
		},
	}
	for name, c := range cases {
		m, got := refusals(t, setEvent(c.set))
		if len(got) != 1 || got[0].Field != c.field || got[0].Event != "skill/set" {
			t.Errorf("%s: published %+v", name, got)
		}
		sel, _ := m.Select("builder", []string{"a.go"}, goTree())
		if !reflect.DeepEqual(names(sel), []string{"standards-comments", "standards-go"}) {
			t.Errorf("%s: table changed: %v", name, names(sel))
		}
	}
}

func TestSetAccepted(t *testing.T) {
	m, got := refusals(
		t,
		setEvent(SkillSet{Name: "standards-go", Enabled: true, Allow: []string{"builder"}, Always: []string{"builder"}}),
	)
	if len(got) != 0 {
		t.Fatalf("refused: %+v", got)
	}
	sel, _ := m.Select("builder", []string{"x.txt"}, goTree())
	if !reflect.DeepEqual(names(sel), []string{"standards-comments", "standards-go"}) {
		t.Fatalf("always skill leads in table order: %v", names(sel))
	}
}

func TestRunAppliesEvents(t *testing.T) {
	RegisterOnce()
	b := bus.New(bus.Options{})
	m := newManager(t, func(d *Deps) { d.Bus = b })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_ = b.Publish(setEvent(SkillSet{Name: "standards-go", Enabled: false, Allow: workers}))
		if _, err := m.Get("builder", "standards-go"); err != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := m.Get("builder", "standards-go"); err == nil {
		t.Fatal("event never applied")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestConnectionsUnknownKey(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(home, "connections.json"),
		[]byte(`{"v":1,"connections":[{"name":"docs","command":"docs-mcp","color":"red"}]}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	conns, warns := loadConnections(home)
	if len(conns) != 0 || len(warns) != 1 || !strings.Contains(warns[0].Path, "connections.json") {
		t.Fatalf("got %v %v", conns, warns)
	}
	m := newManager(t, func(d *Deps) { d.Home = home })
	c := get(t, m, "builder", goTree(), "a.go")
	if len(c.Tools) != 1 || c.Tools[0].Name != "komodo" || len(c.Warnings) != 1 {
		t.Fatalf("tools %+v warnings %+v", c.Tools, c.Warnings)
	}
}

func writeConnections(t *testing.T, home, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "connections.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const docsConn = `{"v":1,"connections":[{"name":"docs","command":"docs-mcp","args":["--stdio"],"env":["DOCS_TOKEN"],` +
	`"enabled":true,"allow":["planner"],"hosts":[]}]}`

func TestConnectionsOffByDefault(t *testing.T) {
	home := t.TempDir()
	writeConnections(t, home, docsConn)
	m := newManager(t, func(d *Deps) { d.Home = home })
	if c := get(t, m, "planner", goTree()); len(c.Tools) != 1 {
		t.Fatalf("an entry from the file is off until a set event: %+v", c.Tools)
	}
	m.apply(bus.Event{
		Type:    bus.TypeMCP,
		Subtype: "set",
		Data:    &MCPSet{Name: "docs", Enabled: true, Allow: []string{"planner"}},
	})
	c := get(t, m, "planner", goTree())
	if len(c.Tools) != 2 || c.Tools[0].Name != "komodo" || c.Tools[1].Name != "docs" || c.Tools[1].Origin != OriginLocal {
		t.Fatalf("tools %+v", c.Tools)
	}
	if !strings.Contains(c.Brief.Parts[0], "docs") || strings.Contains(c.Brief.Parts[0], "DOCS_TOKEN") {
		t.Fatalf("part 1: %q", c.Brief.Parts[0])
	}
	if b := get(t, m, "builder", goTree(), "a.go"); len(b.Tools) != 1 {
		t.Fatalf("builder is not in allow: %+v", b.Tools)
	}
	m.apply(bus.Event{Type: bus.TypeMCP, Subtype: "set", Data: &MCPSet{Name: "komodo", Enabled: false, Allow: nil}})
	if c := get(t, m, "planner", goTree()); c.Tools[0].Name != "komodo" || !c.Tools[0].Enabled {
		t.Fatal("the embedded entry cannot be switched off")
	}
	m.apply(bus.Event{
		Type:    bus.TypeMCP,
		Subtype: "set",
		Data:    &MCPSet{Name: "docs", Enabled: true, Allow: []string{"janitor"}},
	})
	if c := get(t, m, "planner", goTree()); len(c.Tools) != 2 {
		t.Fatal("a refused mcp event must leave the table unchanged")
	}
}

func TestReloadEvents(t *testing.T) {
	home := t.TempDir()
	m := newManager(t, func(d *Deps) { d.Home = home })
	writeConnections(t, home, docsConn)
	m.apply(bus.Event{Type: bus.TypeMCP, Subtype: "reload", Data: &MCPReload{}})
	m.apply(bus.Event{
		Type:    bus.TypeMCP,
		Subtype: "set",
		Data:    &MCPSet{Name: "docs", Enabled: true, Allow: []string{"planner"}},
	})
	if c := get(t, m, "planner", goTree()); len(c.Tools) != 2 {
		t.Fatalf("tools after reload %+v", c.Tools)
	}
	writeLocalSkill(t, home, "standards-mine", localSkillText("standards-mine", ""))
	m.apply(bus.Event{Type: bus.TypeSkill, Subtype: "reload", Data: &SkillReload{}})
	m.apply(setEvent(SkillSet{Name: "standards-mine", Enabled: true, Allow: workers}))
	sel, err := m.Select("builder", []string{"a.go"}, goTree())
	if err != nil || !reflect.DeepEqual(names(sel), []string{"standards-comments", "standards-go", "standards-mine"}) {
		t.Fatalf("after reload: %v %v", names(sel), err)
	}
	m.apply(bus.Event{Type: bus.TypeSkill, Subtype: "reload", Data: &SkillReload{}})
	if sel, _ := m.Select("builder", []string{"a.go"}, goTree()); len(sel) != 3 {
		t.Fatalf("a reload keeps the state of a skill that is still there: %v", names(sel))
	}
}
