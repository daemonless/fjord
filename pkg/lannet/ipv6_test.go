package lannet

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/hostnet"
)

func parseConf(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("conflist is not valid JSON: %v\n%s", err, b)
	}
	return m
}

// A pool network with both families: host-local takes one range LIST per
// family, and needs a route per family or the container gets a v6 address it
// cannot leave the segment with.
func TestConflistDualStackPool(t *testing.T) {
	out, err := Conflist(engine.NetworkSpec{
		Name: "n", Parent: "br0", AddressSource: "pool",
		Subnet: "10.77.0.0/24", Gateway: "10.77.0.1",
		Subnet6: "fd00:4:103::/64", Gateway6: "fd00:4:103::1",
	})
	if err != nil {
		t.Fatal(err)
	}
	conf := parseConf(t, out)
	ipam := conf["plugins"].([]any)[0].(map[string]any)["ipam"].(map[string]any)
	if n := len(ipam["ranges"].([]any)); n != 2 {
		t.Errorf("want 2 range lists (one per family), got %d\n%s", n, out)
	}
	if n := len(ipam["routes"].([]any)); n != 2 {
		t.Errorf("want a default route per family, got %d\n%s", n, out)
	}
	// And it reads back with both halves.
	got, ok := hostnet.Parse("n", out)
	if !ok {
		t.Fatal("does not parse back")
	}
	if got.Subnet != "10.77.0.0/24" || got.Subnet6 != "fd00:4:103::/64" {
		t.Errorf("read back v4=%q v6=%q", got.Subnet, got.Subnet6)
	}
	if got.Gateway6 != "fd00:4:103::1" {
		t.Errorf("v6 gateway lost: %q", got.Gateway6)
	}
}

// A static network records the segment rather than allocating from it; the v6
// half is recorded the same way, since appjail needs a prefix length.
func TestConflistStaticCarriesV6(t *testing.T) {
	out, err := Conflist(engine.NetworkSpec{
		Name: "n", Parent: "br0", AddressSource: "static",
		Subnet: "192.168.4.0/24", Subnet6: "fd00::/64", Gateway6: "fd00::1",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := hostnet.Parse("n", out)
	if got.Subnet6 != "fd00::/64" || got.Gateway6 != "fd00::1" {
		t.Errorf("v6 segment not recorded: %+v\n%s", got, out)
	}
	if !got.Static {
		t.Error("should still read as static")
	}
}

// DHCP cannot carry v6: its addresses come from the CNI dhcp plugin, which is
// v4-only, and one plugin cannot run two IPAMs.
func TestConflistDHCPRefusesV6(t *testing.T) {
	_, err := Conflist(engine.NetworkSpec{
		Name: "n", Parent: "br0", AddressSource: "dhcp", Subnet6: "fd00::/64",
	})
	if err == nil {
		t.Fatal("accepted a v6 segment on a DHCP network")
	}
	if !strings.Contains(err.Error(), "IPv4-only") {
		t.Errorf("error should say why: %v", err)
	}
}

func TestConflistRejectsBadV6(t *testing.T) {
	for _, tc := range []struct{ subnet6, gateway6, want string }{
		{"10.0.0.0/24", "", "invalid IPv6 subnet"}, // a v4 CIDR is not a v6 one
		{"fd00::/64", "10.0.0.1", "invalid IPv6 gateway"},
		{"fd00::/64", "fd01::1", "is not inside"},
		{"garbage", "", "invalid IPv6 subnet"},
	} {
		_, err := Conflist(engine.NetworkSpec{
			Name: "n", Parent: "br0", AddressSource: "pool",
			Subnet: "10.0.0.0/24", Gateway: "10.0.0.1",
			Subnet6: tc.subnet6, Gateway6: tc.gateway6,
		})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("subnet6=%q gateway6=%q: got %v, want %q", tc.subnet6, tc.gateway6, err, tc.want)
		}
	}
}
