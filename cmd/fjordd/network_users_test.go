package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/daemonless/fjord/pkg/stack"
)

// A stopped stack is attached to nothing, so the engines report its network as
// unused and the page offered to delete it -- which stranded the stack on a
// name that no longer resolves. What a stack is CONFIGURED to join is the
// question, and only the compose and the director answer it.
func TestStackNetworkUsers(t *testing.T) {
	dir := t.TempDir()
	write := func(name, file, body string) {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// compose stack on "lan6"
	write("web", "compose.yaml", "services:\n  app:\n    image: x\n    networks:\n      - lan6\nnetworks:\n  lan6:\n    external: true\n")
	// director stack on "ajnet"
	write("jailed", "compose.yaml", "services:\n  app:\n    image: x\n")
	write("jailed", "appjail-director.yml", "options:\n  - virtualnet: 'ajnet:jailed address:10.0.0.7'\nservices:\n  app:\n    name: jailed_app\n")
	// on no network at all
	write("plain", "compose.yaml", "services:\n  app:\n    image: x\n")
	// mixed per-service stack (immich-style: web on lan6, db on immich_priv)
	write("mixed", "compose.yaml", "services:\n  web:\n    image: x\n    networks:\n      - lan6\n  db:\n    image: y\n    networks:\n      - immich_priv\nnetworks:\n  lan6:\n    external: true\n  immich_priv:\n    external: true\n")

	s := &server{manager: stack.NewManager(dir)}
	users := s.stackNetworkUsers()

	for net, want := range map[string][]string{"lan6": {"mixed", "web"}, "ajnet": {"jailed"}, "immich_priv": {"mixed"}} {
		got := users[net]
		if len(got) != len(want) {
			t.Errorf("%s: got %v, want %v", net, got, want)
		}
	}
	for _, net := range []string{"nosuch", "bridge"} {
		if got := users[net]; len(got) != 0 {
			t.Errorf("%s should have no users, got %v", net, got)
		}
	}
	// The whole point: this must work off the files, not off anything the
	// engine reports, since nothing here is running.
	if len(users) != 3 {
		t.Errorf("want lan6, ajnet and immich_priv, got %v", users)
	}
}
