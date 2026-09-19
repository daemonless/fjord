package compose

import (
	"strings"
	"testing"
)

// organizr on jupiter, verbatim: the Resources tab showed "Host ports
// (default)" for this because nothing ever read the network back out.
const organizrCompose = `services:
  organizr:
    image: ghcr.io/daemonless/organizr:latest
    container_name: organizr
    networks:
      vlan5:
        ipv4_address: 192.168.5.20

networks:
  vlan5:
    external: true
`

func TestAttachedNetwork(t *testing.T) {
	cases := []struct {
		name, yaml, wantNet, wantIP string
	}{
		{"mapping with ip", organizrCompose, "vlan5", "192.168.5.20"},
		{"mapping without ip", `services:
  a:
    networks:
      vlan5: {}
`, "vlan5", ""},
		{"list form", `services:
  a:
    networks: [vlan5]
`, "vlan5", ""},
		{"no networks", `services:
  a:
    image: x
`, "", ""},
		// network_mode is not a named network; showing one would let a save
		// rewrite it into something else.
		{"network_mode host", `services:
  a:
    network_mode: host
`, "", ""},
		{"services disagree", `services:
  a:
    networks: [vlan5]
  b:
    networks: [lan86]
`, "", ""},
		{"same net, different ips", `services:
  a:
    networks:
      vlan5:
        ipv4_address: 192.168.5.20
  b:
    networks:
      vlan5:
        ipv4_address: 192.168.5.21
`, "vlan5", ""},
		{"garbage", "\t not yaml {{{", "", ""},
	}
	for _, c := range cases {
		net, ip := AttachedNetwork(c.yaml)
		if net != c.wantNet || ip != c.wantIP {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", c.name, net, ip, c.wantNet, c.wantIP)
		}
	}
}

// What InjectNetwork writes must read back identically.
func TestAttachedNetworkRoundTrip(t *testing.T) {
	out, err := InjectNetwork("services:\n  app:\n    image: x\n", "vlan4", "192.168.4.7", "")
	if err != nil {
		t.Fatalf("InjectNetwork: %v", err)
	}
	if net, ip := AttachedNetwork(out); net != "vlan4" || ip != "192.168.4.7" {
		t.Errorf("round trip gave (%q, %q), want (vlan4, 192.168.4.7)\n%s", net, ip, out)
	}
}

// immich, verbatim from the catalog: four services, all on host networking,
// addressing each other over localhost. Attaching a network would give each
// its own address and break every localhost reference -- and `networks:`
// alongside `network_mode:` is not even valid compose.
const immichCompose = `services:
  immich-server:
    image: ghcr.io/daemonless/immich-server:latest
    network_mode: host
    environment:
      DB_HOSTNAME: localhost
      REDIS_HOSTNAME: localhost
  immich-machine-learning:
    image: ghcr.io/daemonless/immich-ml:latest
    network_mode: host
  redis:
    image: ghcr.io/daemonless/redis:latest
    network_mode: host
  database:
    image: ghcr.io/daemonless/immich-postgres:latest
    network_mode: host
`

// Refused because there are FOUR of them: one service on host mode is just a
// picker choice, and attaching is how it is undone (TestNetworkModeRoundTrip).
func TestInjectNetworkRefusesNetworkMode(t *testing.T) {
	_, err := InjectNetwork(immichCompose, "lan86", "192.168.86.244", "")
	if err == nil {
		t.Fatal("accepted a host-networked stack")
	}
	for _, want := range []string{"network_mode", "localhost"} {
		if !contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
	// Also refused without an IP: the injected compose would be invalid.
	if _, err := InjectNetwork(immichCompose, "lan86", "", ""); err == nil {
		t.Error("accepted a host-networked stack when no IP was given")
	}
	if !NoNamedNetworks(immichCompose) {
		t.Error("not reported as locked, so the UI would offer what Save refuses")
	}
	// One service on host mode is not locked: nothing is reaching anything
	// else over localhost, so attaching is safe and is how the user gets out.
	solo := "services:\n  app:\n    image: x\n    network_mode: host\n"
	if NoNamedNetworks(solo) {
		t.Error("a one-service host stack should not be locked")
	}
	if out, err := InjectNetwork(solo, "lan86", "", ""); err != nil {
		t.Errorf("refused a one-service host stack: %v", err)
	} else if contains(out, "network_mode") {
		t.Errorf("network_mode survived the attach:\n%s", out)
	}
}

// Several services, one of them published: the fixed address goes to that one
// and the rest join the network so they can still reach it.
func TestInjectNetworkMultiServiceIP(t *testing.T) {
	in := `services:
  web:
    image: web
    ports:
      - "8080:80"
  db:
    image: db
  cache:
    image: cache
`
	out, err := InjectNetwork(in, "vlan5", "192.168.5.50", "")
	if err != nil {
		t.Fatalf("InjectNetwork: %v", err)
	}
	if net, ip := AttachedNetwork(out); net != "vlan5" || ip != "" {
		// AttachedNetwork blanks the IP when services differ, which is right:
		// only "web" has one.
		t.Logf("attached: %q %q", net, ip)
	}
	if !contains(out, "ipv4_address: 192.168.5.50") {
		t.Errorf("fixed address missing:\n%s", out)
	}
	if strings.Count(out, "ipv4_address") != 1 {
		t.Errorf("address applied to more than one service:\n%s", out)
	}
	// db and cache must still be on the network, or they cannot reach web.
	if strings.Count(out, "vlan5") < 4 { // top-level + 3 services
		t.Errorf("not every service joined the network:\n%s", out)
	}
}

// No published service (or several): there is nothing to pin the address to,
// and the message has to say what to do instead.
func TestInjectNetworkAmbiguousIP(t *testing.T) {
	in := `services:
  a:
    image: a
  b:
    image: b
`
	_, err := InjectNetwork(in, "vlan5", "192.168.5.50", "")
	if err == nil {
		t.Fatal("accepted an ambiguous fixed IP")
	}
	if !contains(err.Error(), "attach without pinning") {
		t.Errorf("error should suggest a way forward, got: %v", err)
	}
	// Without an IP the same stack attaches fine.
	if _, err := InjectNetwork(in, "vlan5", "", ""); err != nil {
		t.Errorf("multi-service attach without an IP should work: %v", err)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
