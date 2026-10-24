package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestNormalizeRemote(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:rdevitto86/komodo.git":     "github.com/rdevitto86/komodo",
		"https://github.com/rdevitto86/komodo.git": "github.com/rdevitto86/komodo",
		"ssh://git@host.example/o/r/":              "host.example/o/r",
	} {
		if got := normalizeRemote(in); got != want {
			t.Errorf("%s = %s, want %s", in, got, want)
		}
	}
}

func TestRepoIDFallsBackToCommonDir(t *testing.T) {
	repo := repoDir(t, "")
	if got := repoID(repo); got != filepath.Join(repo, ".git") {
		t.Fatalf("no remote: %s", got)
	}
	cfg := "[remote \"origin\"]\n\turl = git@github.com:a/b.git\n"
	if err := os.WriteFile(filepath.Join(repo, ".git", "config"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := repoID(repo); got != "github.com/a/b" {
		t.Fatalf("origin: %s", got)
	}
}

func TestDistanceAndClosest(t *testing.T) {
	if distance("rnu", "run") != 2 || distance("run", "run") != 0 {
		t.Fatal("distance wrong")
	}
	if n, ok := closest("stauts", []string{"status", "stop"}); !ok || n != "status" {
		t.Fatalf("closest = %s, %v", n, ok)
	}
	if _, ok := closest("zzzzzz", []string{"run"}); ok {
		t.Fatal("far name matched")
	}
}

func TestNewIDIsVersion7(t *testing.T) {
	a, b := newID(), newID()
	if !runIDShape.MatchString(a) || a == b {
		t.Fatalf("ids = %s, %s", a, b)
	}
}

func TestSchemaOfUsesFieldTags(t *testing.T) {
	s := schemaOf(reflect.TypeOf(StopData{}))
	props := s["properties"].(map[string]any)
	if _, ok := props["run_id"]; !ok || s["type"] != "object" {
		t.Fatalf("schema = %v", s)
	}
	if req := s["required"].([]string); len(req) != 1 || req[0] != "run_id" {
		t.Fatalf("required = %v", req)
	}
}

func TestInterleavedFlagsAfterPositionals(t *testing.T) {
	fs := newFlagSet(findCommand("run"))
	pos, err := parseInterleaved(fs, []string{"tg-1.2", "--approve", "extra"})
	if err != nil || len(pos) != 2 || pos[0] != "tg-1.2" || fs.Lookup("approve").Value.String() != "true" {
		t.Fatalf("pos = %v, err = %v", pos, err)
	}
}

func TestHasControllingTerminalDoesNotPanic(t *testing.T) {
	_ = HasControllingTerminal()
}
