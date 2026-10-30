package main

import (
	"os"
	"path/filepath"
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
