package podman

import (
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/engine"
)

func TestLANNetavarkArgs(t *testing.T) {
	pool := engine.NetworkSpec{Name: "lan", Kind: "lan", Parent: "lanbridge", Subnet: "192.168.4.0/24", Gateway: "192.168.4.1",
		RangeStart: "192.168.4.200", RangeEnd: "192.168.4.220", MTU: 9000, For: "podman"}
	args, err := lanNetavarkArgs(pool)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(args, " ")
	want := "network create -d bridge --opt mode=unmanaged --interface-name lanbridge --disable-dns" +
		" --subnet 192.168.4.0/24 --gateway 192.168.4.1 --ip-range 192.168.4.200-192.168.4.220" +
		" --opt mtu=9000 --label fjord.for=podman lan"
	if got != want {
		t.Errorf("pool:\n got %s\nwant %s", got, want)
	}

	static := engine.NetworkSpec{Name: "pins", Parent: "lanbridge", AddressSource: "static", Subnet: "192.168.4.0/24",
		Subnet6: "fd00:4::/64", Gateway6: "fd00:4::1"}
	args, err = lanNetavarkArgs(static)
	if err != nil {
		t.Fatal(err)
	}
	got = strings.Join(args, " ")
	for _, part := range []string{"--label fjord.static=true", "--ipv6 --subnet fd00:4::/64 --gateway fd00:4::1"} {
		if !strings.Contains(got, part) {
			t.Errorf("static: %q lacks %q", got, part)
		}
	}

	for name, bad := range map[string]engine.NetworkSpec{
		"no bridge":       {Name: "l", Subnet: "192.168.4.0/24", Gateway: "192.168.4.1"},
		"no gateway":      {Name: "l", Parent: "lanbridge", Subnet: "192.168.4.0/24"},
		"gateway outside": {Name: "l", Parent: "lanbridge", Subnet: "192.168.4.0/24", Gateway: "10.0.0.1"},
		"half a range":    {Name: "l", Parent: "lanbridge", Subnet: "192.168.4.0/24", Gateway: "192.168.4.1", RangeStart: "192.168.4.9"},
		"range outside":   {Name: "l", Parent: "lanbridge", Subnet: "192.168.4.0/24", Gateway: "192.168.4.1", RangeStart: "192.168.5.9", RangeEnd: "192.168.5.20"},
		"bridge name":     {Name: "l", Parent: "lan;rm", Subnet: "192.168.4.0/24", Gateway: "192.168.4.1"},
	} {
		if _, err := lanNetavarkArgs(bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	args, err = lanNetavarkArgs(engine.NetworkSpec{Name: "lan", Parent: "lanbridge", AddressSource: "dhcp", Subnet: "192.168.86.0/24", Gateway: "192.168.86.1", MTU: 9000})
	want = "network create -d epair --interface-name lanbridge --ipam-driver none --opt mtu=9000" +
		" --label fjord.subnet=192.168.86.0/24 --label fjord.gateway=192.168.86.1 lan"
	if err != nil || strings.Join(args, " ") != want {
		t.Errorf("dhcp:\n got %s (%v)\nwant %s", strings.Join(args, " "), err, want)
	}
}

func TestConflistVLAN(t *testing.T) {
	for conf, want := range map[string][2]string{
		`{"plugins":[{"type":"epair","master":"br0","vlan":4,"ipam":{"type":"host-local"}}]}`:                     {"4", ""},
		`{"plugins":[{"type":"epair","master":"br0","vlan":"4","taggedVlans":"3,10-20","ipam":{"type":"dhcp"}}]}`: {"4", "3,10-20"},
		`{"plugins":[{"type":"epair","master":"br0","ipam":{"type":"dhcp"}}]}`:                                    {"", ""},
		`not json`: {"", ""},
	} {
		if vlan, tagged := conflistVLAN([]byte(conf)); vlan != want[0] || tagged != want[1] {
			t.Errorf("%s: %q %q, want %q", conf, vlan, tagged, want)
		}
	}
}

func TestWithVLAN(t *testing.T) {
	pool, _ := lanNetavarkArgs(engine.NetworkSpec{Name: "v4", Parent: "br0", Subnet: "192.168.4.0/24", Gateway: "192.168.4.1",
		RangeStart: "192.168.4.200", RangeEnd: "192.168.4.220"})
	got := strings.Join(withVLAN(pool, "4", "3,10-20"), " ")
	want := "network create -d epair --interface-name br0 --disable-dns --subnet 192.168.4.0/24 --gateway 192.168.4.1" +
		" --ip-range 192.168.4.200-192.168.4.220 --opt vlan=4 --opt taggedVlans=3,10-20 v4"
	if got != want {
		t.Errorf("pool with VLANs:\n got %s\nwant %s", got, want)
	}
	dhcp, _ := lanNetavarkArgs(engine.NetworkSpec{Name: "v4", Parent: "br0", AddressSource: "dhcp"})
	if got := strings.Join(withVLAN(dhcp, "4", ""), " "); got != "network create -d epair --interface-name br0 --ipam-driver none --opt vlan=4 v4" {
		t.Errorf("dhcp with a VLAN: %s", got)
	}
	if got := withVLAN(pool, "", ""); strings.Join(got, " ") != strings.Join(pool, " ") {
		t.Errorf("no VLAN changed the arguments: %v", got)
	}
	// Nothing but what ifconfig's list syntax needs reaches the command line.
	if got := strings.Join(withVLAN(pool, "4; rm -rf /", "$(x)"), " "); got != strings.Join(pool, " ") {
		t.Errorf("unsafe VLAN values were passed on: %s", got)
	}
}
