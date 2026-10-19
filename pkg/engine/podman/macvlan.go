package podman

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/hostnet"
)

// macvlanArgs is the `podman network create` line for a LAN network on
// Linux: kernel macvlan on a host interface, which gives each container its
// own MAC and address on that interface's segment. Addresses come from the
// segment's DHCP server (netavark's dhcp-proxy asks it) or from a pool this
// host hands out. fjord's facts ride in labels, which hostnet reads back.
func macvlanArgs(spec engine.NetworkSpec) ([]string, error) {
	if !hostnet.NameRe.MatchString(spec.Name) {
		return nil, fmt.Errorf("invalid network name %q: letters, digits, dot, dash and underscore only", spec.Name)
	}
	if spec.Parent == "" || !ifaceNameRe.MatchString(spec.Parent) {
		return nil, fmt.Errorf("pick the host interface the network is on")
	}
	args := []string{"network", "create", "--driver", "macvlan", "--opt", "parent=" + spec.Parent}
	if spec.MTU > 0 {
		args = append(args, "--opt", "mtu="+strconv.Itoa(spec.MTU))
	}
	if spec.For != "" {
		args = append(args, "--label", "fjord.for="+spec.For)
	}
	switch spec.AddressSource {
	case "dhcp":
		args = append(args, "--ipam-driver", "dhcp")
		// What the wire is, written down so a stack can later pin an
		// address on it; the lease carries the rest.
		if spec.Subnet != "" {
			if _, _, err := net.ParseCIDR(spec.Subnet); err != nil {
				return nil, fmt.Errorf("invalid subnet %q", spec.Subnet)
			}
			args = append(args, "--label", "fjord.subnet="+spec.Subnet)
			if spec.Gateway != "" {
				args = append(args, "--label", "fjord.gateway="+spec.Gateway)
			}
		}
	case "", "pool":
		if _, _, err := net.ParseCIDR(spec.Subnet); err != nil {
			return nil, fmt.Errorf("a pool network needs the segment's subnet, like 192.168.4.0/24")
		}
		args = append(args, "--subnet", spec.Subnet)
		if spec.Gateway != "" {
			if net.ParseIP(spec.Gateway) == nil {
				return nil, fmt.Errorf("invalid gateway %q", spec.Gateway)
			}
			args = append(args, "--gateway", spec.Gateway)
		}
		if spec.RangeStart != "" || spec.RangeEnd != "" {
			if net.ParseIP(spec.RangeStart) == nil || net.ParseIP(spec.RangeEnd) == nil {
				return nil, fmt.Errorf("a range needs both its first and last address")
			}
			args = append(args, "--ip-range", spec.RangeStart+"-"+spec.RangeEnd)
		}
		if spec.Subnet6 != "" {
			args = append(args, "--ipv6", "--subnet", spec.Subnet6)
			if spec.Gateway6 != "" {
				args = append(args, "--gateway", spec.Gateway6)
			}
		}
	default:
		return nil, fmt.Errorf("addresses %q are not offered on Linux yet: pick DHCP or a pool", spec.AddressSource)
	}
	return append(args, spec.Name), nil
}

// hostIface is what a host interface says about itself, for lanParents.
type hostIface struct {
	Name  string
	Addrs []string // CIDRs, as net.Interface.Addrs gives them
}

// virtualPrefixes are interfaces no LAN network belongs on: the engine's own
// bridges and veths, VPNs, other runtimes'.
var virtualPrefixes = []string{"lo", "podman", "veth", "cni", "docker", "virbr", "tailscale", "wg", "br-", "netavark"}

// lanParents are the host interfaces a macvlan network can hang off, with
// the segment the host already has on each, so the form can prefill it.
func lanParents(ifs []hostIface, routeIface, gateway string) []engine.NetworkParent {
	var out []engine.NetworkParent
	for _, i := range ifs {
		skip := false
		for _, p := range virtualPrefixes {
			if strings.HasPrefix(i.Name, p) {
				skip = true
			}
		}
		if skip {
			continue
		}
		p := engine.NetworkParent{Name: i.Name}
		for _, a := range i.Addrs {
			ip, n, err := net.ParseCIDR(a)
			if err != nil || ip.To4() == nil {
				continue
			}
			p.HostIP, p.Subnet = ip.String(), n.String()
			break
		}
		if i.Name == routeIface {
			p.Gateway = gateway
		}
		out = append(out, p)
	}
	return out
}
