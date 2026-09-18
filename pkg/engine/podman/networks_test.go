package podman

import "testing"

// Real libpod /networks/json fixture (trimmed): a user bridge, two Linux
// macvlans, and the default podman bridge. Everything a stack can be attached
// to survives; only the runtime's own default network is dropped, because
// every stack that asks for nothing is already on it.
const networksFixture = `[
  {"name":"booklore-internal","driver":"bridge","subnets":[{"subnet":"10.89.1.0/24","gateway":"10.89.1.1"}]},
  {"name":"vlan4","driver":"macvlan","subnets":[{"subnet":"192.168.4.0/24","gateway":"192.168.4.1"}]},
  {"name":"vlan5","driver":"macvlan","subnets":[{"subnet":"192.168.5.0/24","gateway":"192.168.5.1"}]},
  {"name":"podman","driver":"bridge","subnets":[{"subnet":"10.88.0.0/16","gateway":"10.88.0.1"}]}
]`

func TestParseNetworksKeepsAttachable(t *testing.T) {
	nets, err := parseNetworks([]byte(networksFixture))
	if err != nil {
		t.Fatalf("parseNetworks: %v", err)
	}
	if len(nets) != 3 {
		t.Fatalf("expected 3 attachable networks, got %d: %+v", len(nets), nets)
	}
	// A user-made bridge is a private network -- the same thing appjail's
	// "nat" kind makes -- so it belongs in the list.
	if nets[0].Name != "booklore-internal" || nets[1].Name != "vlan4" || nets[2].Name != "vlan5" {
		t.Fatalf("unexpected names: %+v", nets)
	}
	for _, n := range nets {
		if n.Name == "podman" {
			t.Fatalf("the default network should not be offered: %+v", nets)
		}
	}
	if nets[2].Subnet != "192.168.5.0/24" || nets[2].Gateway != "192.168.5.1" {
		t.Fatalf("subnet/gateway not extracted: %+v", nets[2])
	}
}

// Verbatim from jupiter: podman on FreeBSD reports ONLY name and driver for a
// third-party CNI plugin's network -- it never reads the conflist, so there is
// no "subnets" key at all. Requiring one (or matching the driver name
// "macvlan") drops every epair network and leaves the picker empty.
const freebsdNetworksFixture = `[
  {"name":"lan86","id":"46e0","driver":"epair","ipv6_enabled":false,"internal":false,"dns_enabled":false},
  {"name":"vlan5","id":"cf08","driver":"epair","ipv6_enabled":false,"internal":false,"dns_enabled":false},
  {"name":"podman","id":"2f25","driver":"bridge","network_interface":"cni-podman0","subnets":[{"subnet":"10.88.0.0/16","gateway":"10.88.0.1"}]}
]`

func TestParseNetworksSubnetlessCNINetworks(t *testing.T) {
	nets, err := parseNetworks([]byte(freebsdNetworksFixture))
	if err != nil {
		t.Fatalf("parseNetworks: %v", err)
	}
	if len(nets) != 2 {
		t.Fatalf("expected lan86 and vlan5, got %d: %+v", len(nets), nets)
	}
	for _, n := range nets {
		if n.Driver != "epair" {
			t.Errorf("kept %q (driver %q); the built-in bridge must be filtered", n.Name, n.Driver)
		}
	}
}

// A driver fjord has never heard of is still attachable if it is not
// host-scoped: appjail reports "virtualnet", Linux reports "macvlan"/"ipvlan".
func TestParseNetworksUnknownDriver(t *testing.T) {
	nets, err := parseNetworks([]byte(`[{"name":"future","driver":"somethingnew"}]`))
	if err != nil {
		t.Fatalf("parseNetworks: %v", err)
	}
	if len(nets) != 1 || nets[0].Name != "future" {
		t.Fatalf("unknown driver was dropped: %+v", nets)
	}
}

func TestParseNetworksEmpty(t *testing.T) {
	nets, err := parseNetworks([]byte(`[]`))
	if err != nil {
		t.Fatalf("parseNetworks: %v", err)
	}
	if len(nets) != 0 {
		t.Fatalf("expected no networks, got %+v", nets)
	}
}

// Verbatim shape from a FreeBSD host: podman-compose creates one bridge
// network per project and labels it. Those are not somewhere to attach a
// stack -- they ARE a stack's own plumbing -- so they must not reach the
// picker, while a bridge network someone made on purpose still does.
const composeProjectFixture = `[
  {"name":"podman","driver":"bridge","subnets":[{"subnet":"10.88.0.0/16","gateway":"10.88.0.1"}]},
  {"name":"zensical_default","driver":"bridge","subnets":[{"subnet":"10.89.0.0/24","gateway":"10.89.0.1"}],
   "labels":{"com.docker.compose.project":"zensical","io.podman.compose.project":"zensical"}},
  {"name":"privnet","driver":"bridge","subnets":[{"subnet":"10.100.0.0/24","gateway":"10.100.0.1"}]},
  {"name":"lan","driver":"epair"}
]`

func TestParseNetworksDropsComposeProjectNetworks(t *testing.T) {
	nets, err := parseNetworks([]byte(composeProjectFixture))
	if err != nil {
		t.Fatalf("parseNetworks: %v", err)
	}
	var names []string
	for _, n := range nets {
		names = append(names, n.Name)
	}
	if len(names) != 2 || names[0] != "privnet" || names[1] != "lan" {
		t.Fatalf("want [privnet lan], got %v", names)
	}
}
