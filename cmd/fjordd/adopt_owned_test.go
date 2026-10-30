package main

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/stack"
)

type jailsEngine struct {
	engine.Backend
	jails []string
}

func (e jailsEngine) StackJails(*stack.Stack) []string { return e.jails }

// The adopt list leaves fjord's own jails out before inspecting them: a
// jail one of its stacks runs, or one in a stack's own director project.
func TestFjordOwns(t *testing.T) {
	stacks := t.TempDir()
	os.MkdirAll(filepath.Join(stacks, "web"), 0o755)
	os.WriteFile(filepath.Join(stacks, "web", "appjail-director.yml"), []byte("services:\n  web:\n    name: web_app\n"), 0o644)
	os.WriteFile(filepath.Join(stacks, "web", "state.json"), []byte(`{"schema_version":1,"engine":"appjail"}`), 0o644)
	s := &server{manager: stack.NewManager(stacks)}
	owns := s.fjordOwns(jailsEngine{engine.Unavailable("appjail"), []string{"web_app"}}, "appjail")
	cases := []struct {
		name, project string
		want          bool
	}{
		{"web_app", "", true},    // a jail the stack runs
		{"web_db", "web", true},  // in the stack's director project
		{"handmade", "", false},  // started outside fjord
		{"other", "gone", false}, // a project fjord has no stack for
	}
	for _, c := range cases {
		if got := owns(c.name, c.project); got != c.want {
			t.Errorf("fjordOwns(%q, %q) = %v, want %v", c.name, c.project, got, c.want)
		}
	}
}

type unmanagedEngine struct {
	engine.Backend
	list []engine.Unmanaged
}

func (e unmanagedEngine) UnmanagedContainers(_ context.Context, skip func(name, project string) bool) ([]engine.Unmanaged, error) {
	var out []engine.Unmanaged
	for _, u := range e.list {
		if !skip(u.Name, u.Project) {
			out = append(out, u)
		}
	}
	return out, nil
}
func (unmanagedEngine) RemoveContainer(context.Context, string) error { return nil }

// The dashboard's line names only what Adopt could take: not a container
// with nothing to recreate it from, not one of fjord's own.
func TestAdoptableNames(t *testing.T) {
	stacks := t.TempDir()
	os.MkdirAll(filepath.Join(stacks, "web"), 0o755)
	os.WriteFile(filepath.Join(stacks, "web", "compose.yaml"), []byte("services:\n  web:\n    image: x\n"), 0o644)
	be := unmanagedEngine{engine.Unavailable("podman"), []engine.Unmanaged{
		{Name: "handmade", RunArgs: []string{"podman", "run", "-d", "--name", "handmade", "nginx"}},
		{Name: "norecord", Unadoptable: "podman kept no run command for this container"},
		{Name: "web", Project: "web"},
	}}
	s := &server{manager: stack.NewManager(stacks), backends: map[string]engine.Backend{"podman": be}}
	w := httptest.NewRecorder()
	s.handleAdoptable(w, httptest.NewRequest("GET", "/api/adopt/names", nil))
	if got := strings.TrimSpace(w.Body.String()); got != `{"names":["handmade"]}` {
		t.Errorf("got %s, want only handmade", got)
	}
}
