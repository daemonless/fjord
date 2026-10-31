package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/hostnet"
	"github.com/daemonless/fjord/pkg/stack"
)

func TestPoolAddresses(t *testing.T) {
	got := poolAddresses(hostnet.Network{Subnet: "192.168.4.0/24", RangeStart: "192.168.4.243", RangeEnd: "192.168.4.245"})
	if want := []string{"192.168.4.243", "192.168.4.244", "192.168.4.245"}; !reflect.DeepEqual(got, want) {
		t.Errorf("range: %v, want %v", got, want)
	}
	got = poolAddresses(hostnet.Network{Subnet: "10.0.0.0/30"})
	if want := []string{"10.0.0.1", "10.0.0.2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("subnet: %v, want %v", got, want)
	}
}

// A pool gave a container .4, which another host on the LAN had. Here
// another machine holds .243, another stack .244 (on a second pool on the
// same wire), and the gateway is .1.
func poolFixture(t *testing.T, held map[string]map[string]map[string]string) *server {
	t.Helper()
	root := t.TempDir()
	conf := filepath.Join(root, "net.d")
	os.MkdirAll(conf, 0o755)
	pool := func(name, start, end string) string {
		return `{"cniVersion":"0.4.0","name":"` + name + `","plugins":[{"type":"macvlan","master":"lanbr","ipam":{"type":"host-local","ranges":[[{"subnet":"192.168.4.0/24","gateway":"192.168.4.1","rangeStart":"` + start + `","rangeEnd":"` + end + `"}]]}}]}`
	}
	os.WriteFile(filepath.Join(conf, "t-pool.conflist"), []byte(pool("t-pool", "192.168.4.243", "192.168.4.246")), 0o644)
	os.WriteFile(filepath.Join(conf, "lanpool.conflist"), []byte(pool("lanpool", "192.168.4.240", "192.168.4.250")), 0o644)
	t.Cleanup(hostnet.UseConflists(conf))
	stacks := filepath.Join(root, "stacks")
	write := func(name, compose string) {
		os.MkdirAll(filepath.Join(stacks, name), 0o755)
		os.WriteFile(filepath.Join(stacks, name, "compose.yaml"), []byte(compose), 0o644)
		os.WriteFile(filepath.Join(stacks, name, "state.json"), []byte(`{"schema_version":1,"engine":"podman"}`), 0o644)
	}
	write("app", "services:\n  web:\n    image: x\n    networks:\n      - t-pool\nnetworks:\n  t-pool:\n    external: true\n")
	write("other", "services:\n  db:\n    image: x\n    networks:\n      lanpool:\n        ipv4_address: 192.168.4.244\nnetworks:\n  lanpool:\n    external: true\n")
	be := liveEngine{engine.Unavailable("podman"), held}
	return &server{manager: stack.NewManager(stacks), fjordRoot: root, backends: map[string]engine.Backend{"podman": be}}
}

func useWire(t *testing.T, fn func(iface, ip string) (bool, bool)) {
	oldA, oldC, oldL := addressAnswers, wireCheckable, linuxHost
	addressAnswers = func(_ context.Context, iface, ip string) (bool, bool) { return fn(iface, ip) }
	wireCheckable = func() bool { return true }
	linuxHost = true
	t.Cleanup(func() { addressAnswers, wireCheckable, linuxHost = oldA, oldC, oldL })
}

func pinnedIP(t *testing.T, s *server, name, svc string) string {
	st, err := s.manager.Get(name)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range composepkg.ServiceAttachments(st.Compose)[svc] {
		return a.IP
	}
	return ""
}

func TestChooseAddressSkipsHeld(t *testing.T) {
	s := poolFixture(t, nil)
	var asked []string
	useWire(t, func(iface, ip string) (bool, bool) {
		asked = append(asked, iface+" "+ip)
		return ip == "192.168.4.243", true
	})
	st, _ := s.manager.Get("app")
	note := s.chooseAddresses(context.Background(), st)
	if got := pinnedIP(t, s, "app", "web"); got != "192.168.4.245" {
		t.Errorf("pinned %q, want .245 (.243 answers, .244 is other's)", got)
	}
	if !strings.Contains(note, "192.168.4.243 answers on the wire, so another machine has it") {
		t.Errorf("note %q does not say why .243 was passed over", note)
	}
	if want := []string{"lanbr 192.168.4.243", "lanbr 192.168.4.245"}; !reflect.DeepEqual(asked, want) {
		t.Errorf("asked %v, want %v (never a taken address)", asked, want)
	}
}

func TestChooseAddressUncheckedLeavesThePool(t *testing.T) {
	s := poolFixture(t, nil)
	useWire(t, func(string, string) (bool, bool) { return false, false })
	wireCheckable = func() bool { return false }
	st, _ := s.manager.Get("app")
	note := s.chooseAddresses(context.Background(), st)
	if !strings.Contains(note, "iputils-arping") {
		t.Errorf("note %q, want it to say what to install", note)
	}
	if got := pinnedIP(t, s, "app", "web"); got != "" {
		t.Errorf("pinned %q with no wire check, want the pool to choose", got)
	}
}

func TestChooseAddressLeavesAHeldAddress(t *testing.T) {
	s := poolFixture(t, map[string]map[string]map[string]string{"app": {"web": {"t-pool": "192.168.4.246"}}})
	useWire(t, func(string, string) (bool, bool) {
		t.Error("the wire was asked for a service that has an address")
		return false, true
	})
	st, _ := s.manager.Get("app")
	s.chooseAddresses(context.Background(), st)
	if got := pinnedIP(t, s, "app", "web"); got != "" {
		t.Errorf("pinned %q, want keepAddresses to do it after start", got)
	}
}

func TestOnlyLANNetworks(t *testing.T) {
	if onTheLAN(hostnet.Network{Type: "bridge"}) || !onTheLAN(hostnet.Network{Type: "macvlan"}) {
		t.Error("only macvlan/ipvlan networks are on the LAN")
	}
}
