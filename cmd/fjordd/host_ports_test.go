package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/daemonless/fjord/pkg/hostnet"
	"github.com/daemonless/fjord/pkg/stack"
)

// Review 2.4: a stack with a private segment still publishes its front
// service on the host. Skipping every stack with a network attached let two
// of them take the same host port unchecked.
func TestHostPublishedPorts(t *testing.T) {
	conf := t.TempDir()
	os.WriteFile(filepath.Join(conf, "vlan5.conflist"), []byte(`{"cniVersion":"0.4.0","name":"vlan5","plugins":[{"type":"epair","master":"vlan5bridge","ipam":{"type":"host-local","ranges":[[{"subnet":"192.168.5.0/24","gateway":"192.168.5.1"}]]}}]}`), 0o644)
	t.Cleanup(hostnet.UseConflists(conf))

	private := "services:\n  web:\n    image: x\n    ports:\n      - \"${WEB_PORT}:80\"\n    networks:\n      - default\n      - app_priv\n  db:\n    image: x\n    networks:\n      - app_priv\nnetworks:\n  app_priv:\n    external: true\n"
	lan := "services:\n  web:\n    image: x\n    ports:\n      - \"8080:80\"\n    networks:\n      vlan5:\n        ipv4_address: 192.168.5.19\nnetworks:\n  vlan5:\n    external: true\n"
	mixed := "services:\n  web:\n    image: x\n    ports:\n      - \"8080:80\"\n    networks:\n      - vlan5\n  admin:\n    image: x\n    ports:\n      - \"9090:90/udp\"\nnetworks:\n  vlan5:\n    external: true\n"

	for _, tc := range []struct {
		name, compose string
		want          []string
	}{
		{"private segment keeps the front port", private, []string{"3130/tcp"}},
		{"own address binds nothing on the host", lan, nil},
		{"only the LAN service is skipped", mixed, []string{"9090/udp"}},
	} {
		var got []string
		for _, p := range hostPublishedPorts(tc.compose, map[string]string{"WEB_PORT": "3130"}) {
			got = append(got, portKey(p))
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}

	stacks := filepath.Join(t.TempDir(), "stacks")
	os.MkdirAll(filepath.Join(stacks, "other"), 0o755)
	os.WriteFile(filepath.Join(stacks, "other", "compose.yaml"), []byte(private), 0o644)
	os.WriteFile(filepath.Join(stacks, "other", ".env"), []byte("WEB_PORT=3130\n"), 0o644)
	os.WriteFile(filepath.Join(stacks, "other", "state.json"), []byte(`{"schema_version":1,"engine":"podman","desired_state":"running"}`), 0o644)
	s := &server{manager: stack.NewManager(stacks)}
	if got := s.otherStackPorts(&stack.Stack{Name: "mine"}); got["3130/tcp"] != "other" {
		t.Errorf("otherStackPorts = %v, want 3130/tcp held by other", got)
	}
}
