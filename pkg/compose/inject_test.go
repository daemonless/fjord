package compose

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The caddy compose from the dogfood session -- single service, a comment, and
// host port publishing that collides on a busy host.
const caddyCompose = `services:
  caddy:
    image: "ghcr.io/daemonless/caddy:latest"
    container_name: caddy
    environment:
      - TZ=UTC  # Timezone for the container
    volumes:
      - "/containers/caddy:/config"
    ports:
      - "80:80"
    restart: unless-stopped
`

// mustParse fails the test if the string isn't valid YAML.
func mustParse(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("result is not valid YAML: %v\n%s", err, s)
	}
	return m
}

func TestInjectNetworkStaticIP(t *testing.T) {
	out, err := InjectNetwork(caddyCompose, "vlan5", "192.168.5.50", "")
	if err != nil {
		t.Fatalf("InjectNetwork: %v", err)
	}
	m := mustParse(t, out)

	// Top-level networks: {vlan5: {external: true}}
	nets, _ := m["networks"].(map[string]any)
	v5, _ := nets["vlan5"].(map[string]any)
	if v5["external"] != true {
		t.Fatalf("top-level networks.vlan5.external not true: %+v", m["networks"])
	}

	// Service attached with the static IP.
	svc := m["services"].(map[string]any)["caddy"].(map[string]any)
	snet := svc["networks"].(map[string]any)["vlan5"].(map[string]any)
	if snet["ipv4_address"] != "192.168.5.50" {
		t.Fatalf("service ipv4_address not set: %+v", svc["networks"])
	}

	// Comment must survive the round-trip.
	if !strings.Contains(out, "# Timezone for the container") {
		t.Fatalf("comment lost in round-trip:\n%s", out)
	}
}

func TestInjectNetworkAutoAssign(t *testing.T) {
	out, err := InjectNetwork(caddyCompose, "vlan5", "", "")
	if err != nil {
		t.Fatalf("InjectNetwork: %v", err)
	}
	m := mustParse(t, out)
	svc := m["services"].(map[string]any)["caddy"].(map[string]any)
	list, ok := svc["networks"].([]any)
	if !ok || len(list) != 1 || list[0] != "vlan5" {
		t.Fatalf("auto-assign should use list form [vlan5], got: %+v", svc["networks"])
	}
}

func TestInjectNetworkIPRequiresSingleService(t *testing.T) {
	multi := `services:
  a:
    image: x
  b:
    image: y
`
	if _, err := InjectNetwork(multi, "vlan5", "192.168.5.50", ""); err == nil {
		t.Fatal("expected error assigning a shared IP across two services")
	}
	// Auto-assign across multiple services is fine.
	if _, err := InjectNetwork(multi, "vlan5", "", ""); err != nil {
		t.Fatalf("auto-assign multi-service should succeed: %v", err)
	}
}

// Attaching has to be repeatable: the Resources tab sends the whole thing
// again to change an address or a MAC, and that arrives with the service
// already on a network.
func TestInjectNetworkReattaches(t *testing.T) {
	withNet := `services:
  a:
    image: x
    networks:
      - other
`
	out, err := InjectNetwork(withNet, "vlan5", "", "02:1a:2b:3c:4d:5e")
	if err != nil {
		t.Fatalf("re-attach refused: %v", err)
	}
	if net, _ := AttachedNetwork(out); net != "vlan5" {
		t.Errorf("network not replaced, got %q:\n%s", net, out)
	}
	if AttachedMAC(out) != "02:1a:2b:3c:4d:5e" {
		t.Errorf("MAC not set:\n%s", out)
	}
	// Clearing the field drops the pin rather than leaving a stale one.
	back, err := InjectNetwork(out, "vlan5", "", "")
	if err != nil {
		t.Fatalf("re-attach without a MAC refused: %v", err)
	}
	if AttachedMAC(back) != "" {
		t.Errorf("stale MAC kept:\n%s", back)
	}
}

// Several networks is a stack fjord can now describe, so the list it is given
// replaces whatever was there -- the caller seeds its table from
// AttachedNetworks, so what gets written is what the user was shown.
func TestInjectNetworksReplacesTheWholeSet(t *testing.T) {
	withNets := `services:
  a:
    image: x
    networks:
      - other
      - second
`
	out, err := InjectNetworks(withNets, []Attachment{
		{Network: "lan", MAC: "02:1a:2b:3c:4d:5e"},
		{Network: "private", IP: "10.100.0.5"},
	})
	if err != nil {
		t.Fatalf("replace refused: %v", err)
	}
	got := AttachedNetworks(out)
	if len(got) != 2 || got[0].Network != "lan" || got[1].Network != "private" {
		t.Fatalf("network set not replaced, got %+v:\n%s", got, out)
	}
	// Order is the interface order, and each keeps its own pin.
	if got[0].MAC != "02:1a:2b:3c:4d:5e" || got[1].IP != "10.100.0.5" {
		t.Errorf("pins not written per network: %+v\n%s", got, out)
	}
	if got[0].IP != "" || got[1].MAC != "" {
		t.Errorf("pins leaked between networks: %+v", got)
	}
}

// A network listed twice would be two interfaces fjord cannot tell apart.
func TestInjectNetworksRejectsDuplicates(t *testing.T) {
	in := "services:\n  a:\n    image: x\n"
	if _, err := InjectNetworks(in, []Attachment{{Network: "lan"}, {Network: "lan"}}); err == nil {
		t.Fatal("expected error when a network is listed twice")
	}
}

func TestSplitPortSpecAndParsePort(t *testing.T) {
	cases := map[string][2]string{
		"80":                {"80", "80"},
		"8080:80":           {"8080", "80"},
		"127.0.0.1:8080:80": {"8080", "80"},
		"[::1]:8443:443":    {"8443", "443"},
		"0.0.0.0:53:53":     {"53", "53"},
	}
	for in, want := range cases {
		h, c := SplitPortSpec(in)
		if h != want[0] || c != want[1] {
			t.Errorf("%q: got %s,%s want %s,%s", in, h, c, want[0], want[1])
		}
	}
	pm, ok := parsePort("127.0.0.1:5432:5432/tcp")
	if !ok || pm.Host != 5432 || pm.Container != 5432 || pm.Proto != "tcp" {
		t.Fatalf("ip-prefixed parsePort: %+v ok=%v", pm, ok)
	}
	ports := PublishedPorts("services:\n  db:\n    ports:\n      - \"127.0.0.1:5432:5432\"\n      - \"8080:80/udp\"\n", nil)
	if len(ports) != 2 || ports[0].Host != 5432 || ports[1].Proto != "udp" {
		t.Fatalf("PublishedPorts: %+v", ports)
	}
}

func TestDropTopLevelKey(t *testing.T) {
	in := "name: custom\nservices:\n  a:\n    image: x\n"
	out := DropTopLevelKey(in, "name")
	if strings.Contains(out, "name: custom") || !strings.Contains(out, "image: x") {
		t.Fatalf("got %q", out)
	}
	if DropTopLevelKey(in, "absent") != in {
		t.Fatal("absent key must leave input unchanged")
	}
}

// Stacks attached before per-network pins carry one service-level
// mac_address; podman-compose applies it to the first network, so reading it
// any other way would lose the pin on the next save.
func TestAttachedNetworksReadsLegacyServiceMAC(t *testing.T) {
	in := `services:
  app:
    image: x
    mac_address: 02:aa:bb:cc:dd:ee
    networks:
      - lan
`
	got := AttachedNetworks(in)
	if len(got) != 1 || got[0].MAC != "02:aa:bb:cc:dd:ee" {
		t.Fatalf("legacy service-level MAC not read: %+v", got)
	}
	// Writing it back moves it onto the network it belongs to.
	out, err := InjectNetworks(in, got)
	if err != nil {
		t.Fatalf("re-inject: %v", err)
	}
	if !strings.Contains(out, "mac_address: 02:aa:bb:cc:dd:ee") || strings.Contains(out, "\n    mac_address:") {
		t.Errorf("MAC not moved under the network:\n%s", out)
	}
}
