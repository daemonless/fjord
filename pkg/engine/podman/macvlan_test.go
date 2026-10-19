package podman

import (
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/engine"
)

func TestMacvlanArgs(t *testing.T) {
	got, err := macvlanArgs(engine.NetworkSpec{Name: "lan", Kind: "lan", Parent: "enp0s6f1", AddressSource: "dhcp", Subnet: "192.168.4.0/24", Gateway: "192.168.4.1", MTU: 1500, For: "podman"})
	want := "network create --driver macvlan --opt parent=enp0s6f1 --opt mtu=1500 --label fjord.for=podman --ipam-driver dhcp --label fjord.subnet=192.168.4.0/24 --label fjord.gateway=192.168.4.1 lan"
	if err != nil || strings.Join(got, " ") != want {
		t.Errorf("dhcp:\n got %q %v\nwant %q", strings.Join(got, " "), err, want)
	}
	got, err = macvlanArgs(engine.NetworkSpec{Name: "pool", Parent: "eth0", AddressSource: "pool", Subnet: "10.5.0.0/24", Gateway: "10.5.0.1", RangeStart: "10.5.0.100", RangeEnd: "10.5.0.150"})
	want = "network create --driver macvlan --opt parent=eth0 --subnet 10.5.0.0/24 --gateway 10.5.0.1 --ip-range 10.5.0.100-10.5.0.150 pool"
	if err != nil || strings.Join(got, " ") != want {
		t.Errorf("pool:\n got %q %v\nwant %q", strings.Join(got, " "), err, want)
	}
	for _, bad := range []engine.NetworkSpec{
		{Name: "x", AddressSource: "dhcp"},                 // no parent
		{Name: "x", Parent: "eth0", AddressSource: "pool"}, // no subnet
		{Name: "x", Parent: "eth0", AddressSource: "static", Subnet: "10.0.0.0/24"},
		{Name: "x", Parent: "eth0;rm", AddressSource: "dhcp"},
	} {
		if _, err := macvlanArgs(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

// netlab-linux's interfaces: two NICs, podman's bridge, a veth, a VPN.
func TestLanParents(t *testing.T) {
	ifs := []hostIface{
		{Name: "lo", Addrs: []string{"127.0.0.1/8"}},
		{Name: "enp0s6f0", Addrs: []string{"192.168.86.194/24", "fe80::1/64"}},
		{Name: "enp0s6f1", Addrs: []string{"192.168.4.180/24"}},
		{Name: "enp0s6f1.5"},
		{Name: "podman0", Addrs: []string{"10.88.0.1/16"}},
		{Name: "veth0"}, {Name: "tailscale0", Addrs: []string{"100.97.72.22/32"}},
	}
	got := lanParents(ifs, "enp0s6f0", "192.168.86.1")
	names := []string{}
	for _, p := range got {
		names = append(names, p.Name)
	}
	if strings.Join(names, ",") != "enp0s6f0,enp0s6f1,enp0s6f1.5" {
		t.Fatalf("parents %v", names)
	}
	if got[0].Subnet != "192.168.86.0/24" || got[0].HostIP != "192.168.86.194" || got[0].Gateway != "192.168.86.1" {
		t.Errorf("uplink: %+v", got[0])
	}
	if got[1].Subnet != "192.168.4.0/24" || got[1].Gateway != "" || got[2].Subnet != "" {
		t.Errorf("others: %+v %+v", got[1], got[2])
	}
}
