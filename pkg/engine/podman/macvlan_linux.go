package podman

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"

	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/hostnet"
)

// lanKind is the LAN network kind on Linux: kernel macvlan on a host
// interface. DHCP needs netavark's dhcp-proxy; without it only a pool is
// offered.
func lanKind() (engine.NetworkKind, bool) {
	return engine.NetworkKind{
		ID:    "lan",
		Label: "macvlan",
		Help: "Containers get their own address on the segment of the interface you pick, so they can bind :80/:443 " +
			"without colliding with the host. The host itself cannot reach them through that interface (macvlan).",
		ParentLabel:   "Interface",
		SupportsDHCP:  dhcpProxyActive(),
		NeedsGateway:  true,
		SupportsMTU:   true,
		SupportsRange: true,
		NoStatic:      true,
	}, true
}

// dhcpProxyActive: netavark asks the segment's DHCP server through its
// dhcp-proxy, socket-activated by systemd.
func dhcpProxyActive() bool {
	out, _ := exec.Command("systemctl", "is-active", "netavark-dhcp-proxy.socket").Output()
	return strings.TrimSpace(string(out)) == "active"
}

func (b *Backend) createMacvlan(ctx context.Context, spec engine.NetworkSpec) (engine.Network, error) {
	if spec.AddressSource == "dhcp" && !dhcpProxyActive() {
		return engine.Network{}, fmt.Errorf("DHCP on a LAN network needs netavark's dhcp-proxy: systemctl enable --now netavark-dhcp-proxy.socket")
	}
	args, err := macvlanArgs(spec)
	if err != nil {
		return engine.Network{}, err
	}
	if out, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput(); err != nil {
		return engine.Network{}, fmt.Errorf("podman network create: %s", strings.TrimSpace(string(out)))
	}
	def, ok := hostnet.Get(spec.Name)
	if !ok {
		return engine.Network{}, fmt.Errorf("podman made %s but its definition is not where podman keeps them", spec.Name)
	}
	return engine.Network{Name: def.Name, Driver: def.Type, Subnet: def.Subnet, Gateway: def.Gateway}, nil
}

// linuxParents reads the host's interfaces and its default route.
func linuxParents() []engine.NetworkParent {
	ifs, _ := net.Interfaces()
	var hs []hostIface
	for _, i := range ifs {
		h := hostIface{Name: i.Name}
		if addrs, err := i.Addrs(); err == nil {
			for _, a := range addrs {
				h.Addrs = append(h.Addrs, a.String())
			}
		}
		hs = append(hs, h)
	}
	iface, gw := defaultRoute()
	return lanParents(hs, iface, gw)
}

// defaultRoute is the interface and gateway of the IPv4 default route, from
// /proc/net/route (addresses there are little-endian hex).
func defaultRoute() (iface, gateway string) {
	b, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return "", ""
	}
	for _, l := range strings.Split(string(b), "\n")[1:] {
		f := strings.Fields(l)
		if len(f) < 3 || f[1] != "00000000" {
			continue
		}
		raw, err := hex.DecodeString(f[2])
		if err != nil || len(raw) != 4 {
			continue
		}
		return f[0], net.IPv4(raw[3], raw[2], raw[1], raw[0]).String()
	}
	return "", ""
}
