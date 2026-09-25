package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/hostnet"
	"github.com/daemonless/fjord/pkg/stack"
)

// liveEngine reports, per stack, the addresses its running containers hold.
type liveEngine struct {
	engine.Backend
	held map[string]map[string]map[string]string // stack -> service -> network -> address
}

func (e liveEngine) Status(_ context.Context, st *stack.Stack) (engine.StackStatus, error) {
	var out engine.StackStatus
	for svc, addrs := range e.held[st.Name] {
		out.Containers = append(out.Containers, engine.ContainerStatus{Name: st.Name + "_" + svc, ID: st.Name + svc, Service: svc, State: "running", Addresses: addrs})
	}
	out.State = "running"
	return out, nil
}

func (e liveEngine) Capabilities() engine.Capabilities {
	return engine.Capabilities{UpdateServices: true}
}

// jupiter on 2026-09-25: smokeping pinned 192.168.5.19 on vlan5; seerr had no
// pin and the pool gave it .19 too.
func clashFixture(t *testing.T) *server {
	t.Helper()
	root := t.TempDir()
	conf := filepath.Join(root, "net.d")
	os.MkdirAll(conf, 0o755)
	os.WriteFile(filepath.Join(conf, "vlan5.conflist"), []byte(`{"cniVersion":"0.4.0","name":"vlan5","plugins":[{"type":"epair","master":"vlan5bridge","ipam":{"type":"host-local","ranges":[[{"subnet":"192.168.5.0/24","gateway":"192.168.5.1"}]]}}]}`), 0o644)
	old := hostnet.ConfDir
	hostnet.ConfDir = conf
	t.Cleanup(func() { hostnet.ConfDir = old })

	stacks := filepath.Join(root, "stacks")
	write := func(name, compose string) {
		os.MkdirAll(filepath.Join(stacks, name), 0o755)
		os.WriteFile(filepath.Join(stacks, name, "compose.yaml"), []byte(compose), 0o644)
		os.WriteFile(filepath.Join(stacks, name, "state.json"), []byte(`{"schema_version":1,"engine":"podman"}`), 0o644)
	}
	write("smokeping", "services:\n  smokeping:\n    image: x\n    networks:\n      vlan5:\n        ipv4_address: 192.168.5.19\nnetworks:\n  vlan5:\n    external: true\n")
	write("seerr", "services:\n  seerr:\n    image: x\n    networks:\n      - vlan5\nnetworks:\n  vlan5:\n    external: true\n")
	be := liveEngine{engine.Unavailable("podman"), map[string]map[string]map[string]string{
		"smokeping": {"smokeping": {"vlan5": "192.168.5.19"}},
		"seerr":     {"seerr": {"vlan5": "192.168.5.19"}},
	}}
	return &server{manager: stack.NewManager(stacks), fjordRoot: root, backends: map[string]engine.Backend{"podman": be}}
}

// The seerr case: pinning at start must not write the clash into seerr's
// compose, and must say why.
func TestKeepAddressesSkipsATakenAddress(t *testing.T) {
	s := clashFixture(t)
	st, _ := s.manager.Get("seerr")
	kept, skipped := s.keepAddresses(context.Background(), st)
	if len(kept) != 0 {
		t.Errorf("pinned a taken address: %v", kept)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "192.168.5.19 on vlan5 is smokeping's address") {
		t.Errorf("skipped: %v", skipped)
	}
	if after, _ := s.manager.Get("seerr"); strings.Contains(after.Compose, "ipv4_address") {
		t.Errorf("seerr's compose was pinned:\n%s", after.Compose)
	}
}

// A stack pinning what another stack holds, pinned or not, is a clash --
// what pre-flight and Save refuse.
func TestClashesPinnedAndLive(t *testing.T) {
	s := clashFixture(t)
	st, _ := s.manager.Get("smokeping")
	c := clashes(st, s.takenAddresses(context.Background(), "smokeping"))
	if len(c) != 1 || !strings.Contains(c[0], "is in use by seerr") || !strings.Contains(c[0], "give smokeping another address") {
		t.Errorf("smokeping vs a live, unpinned seerr: %v", c)
	}

	pinned := &stack.Stack{Name: "newapp", Compose: "services:\n  web:\n    image: x\n    networks:\n      vlan5:\n        ipv4_address: 192.168.5.19\n"}
	if c := clashes(pinned, s.takenAddresses(context.Background(), "newapp")); len(c) != 1 || !strings.Contains(c[0], "smokeping's address") {
		t.Errorf("a new stack pinning smokeping's address: %v", c)
	}
	free := &stack.Stack{Name: "newapp", Compose: strings.Replace(pinned.Compose, ".19", ".26", 1)}
	if c := clashes(free, s.takenAddresses(context.Background(), "newapp")); len(c) != 0 {
		t.Errorf("a free address clashed: %v", c)
	}
}
