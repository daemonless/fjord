package appjail

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/hostnet"
	"github.com/daemonless/fjord/pkg/lannet"
)

// networkKinds is appjail's contribution to Capabilities.
//
// Both kinds. "nat" is appjail's own: a private network it creates and owns,
// gateway and all, which is safe precisely because the segment is new.
//
// "lan" is the host's, not appjail's -- defining one writes a conflist, it
// does not run `appjail network add`, which would put the segment's gateway
// address (the router's) on a bridge it made. appjail always supports DHCP on
// one: it runs dhclient inside the jail, so unlike podman there is no plugin
// feature to check.
var netNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)

func networkKinds() []engine.NetworkKind {
	return []engine.NetworkKind{lannet.Kind(true), {
		ID:                  "nat",
		Label:               "bridge",
		Help:                "The engine creates the bridge and hands out addresses. Jails reach the outside through the host; nothing on your LAN can reach them directly.",
		AddressNote:         "Addresses come from this host: a segment the engine invents has no DHCP server to ask.",
		SupportsMTU:         true,
		SupportsDescription: true,
	}}
}

// CreateNetwork adds an appjail virtualnet.
func (b *Backend) CreateNetwork(ctx context.Context, spec engine.NetworkSpec) (engine.Network, error) {
	if spec.Kind == "lan" {
		// Host state, written the same way whichever engine asked.
		return lannet.Create(spec)
	}
	if spec.Kind != "" && spec.Kind != "nat" {
		return engine.Network{}, fmt.Errorf("unknown network kind %q", spec.Kind)
	}
	if !netNameRe.MatchString(spec.Name) {
		return engine.Network{}, fmt.Errorf("invalid network name %q: letters, digits, dot, dash and underscore only", spec.Name)
	}
	ip, cidr, err := net.ParseCIDR(spec.Subnet)
	if err != nil {
		return engine.Network{}, fmt.Errorf("invalid subnet %q: want a CIDR like 10.100.0.0/24", spec.Subnet)
	}
	// appjail claims the first address of whatever it is given. On a segment
	// that already exists that address belongs to the router, so refuse it.
	if !ip.IsPrivate() {
		return engine.Network{}, fmt.Errorf("%s is not a private range: a network appjail creates must be one it owns (10.x, 172.16-31.x, 192.168.x)", spec.Subnet)
	}
	if clash := overlapsHost(ctx, cidr); clash != "" {
		return engine.Network{}, fmt.Errorf("%s overlaps %s, a segment this host is already on: appjail would claim its gateway address. Pick an unused range", spec.Subnet, clash)
	}
	// The address check above only sees segments this host has an address on,
	// and a VLAN bridge built for containers usually has none. So ask the wire
	// itself: appjail is about to take the first address, and if something
	// already answers there, that something is almost certainly the router.
	gw := firstAddr(cidr)
	if answers(ctx, gw) {
		return engine.Network{}, fmt.Errorf("%s is already in use: something answers at %s, which is the address appjail would claim as this network's gateway. Pick a range nothing else is on", spec.Subnet, gw)
	}
	// appjail names the bridge after the network, and destroying it later
	// refuses if that name belongs to something else.
	if interfaceExists(ctx, spec.Name) {
		return engine.Network{}, fmt.Errorf("an interface named %q already exists; appjail would create a bridge of that name. Choose a different network name", spec.Name)
	}
	for _, n := range listVirtualnets(ctx) {
		if n.Name == spec.Name {
			return engine.Network{}, fmt.Errorf("a network named %q already exists", spec.Name)
		}
	}
	args := []string{"network", "add"}
	if spec.MTU > 0 {
		args = append(args, "-m", strconv.Itoa(spec.MTU))
	}
	if spec.Description != "" {
		args = append(args, "-d", spec.Description)
	}
	args = append(args, spec.Name, spec.Subnet)
	if out, err := exec.CommandContext(ctx, "appjail", args...).CombinedOutput(); err != nil {
		return engine.Network{}, fmt.Errorf("appjail network add: %w: %s", err, strings.TrimSpace(string(out)))
	}
	// Read it back: appjail derives the gateway, so the spec does not know it.
	for _, n := range listVirtualnets(ctx) {
		if n.Name == spec.Name {
			return n, nil
		}
	}
	return engine.Network{Name: spec.Name, Driver: "virtualnet", Subnet: spec.Subnet}, nil
}

// overlapsHost names a segment the host already has an address on that the
// given range would collide with, or "".
func overlapsHost(ctx context.Context, cidr *net.IPNet) string {
	out, err := exec.CommandContext(ctx, "ifconfig", "-a").Output()
	if err != nil {
		return ""
	}
	iface := ""
	for _, ln := range strings.Split(string(out), "\n") {
		if len(ln) > 0 && ln[0] != ' ' && ln[0] != '\t' {
			iface = strings.TrimSuffix(strings.Fields(ln)[0], ":")
			continue
		}
		f := strings.Fields(ln)
		if len(f) < 2 || f[0] != "inet" {
			continue
		}
		ip := net.ParseIP(f[1])
		if ip != nil && !ip.IsLoopback() && cidr.Contains(ip) {
			return fmt.Sprintf("%s on %s", ip, iface)
		}
	}
	return ""
}

// RemoveNetwork removes a virtualnet. appjail refuses one with jails attached
// unless forced, so its own error is mapped to ErrInUse for the 409.
func (b *Backend) RemoveNetwork(ctx context.Context, name string, force bool) error {
	if !netNameRe.MatchString(name) {
		return fmt.Errorf("invalid network name %q", name)
	}
	if def, ok := hostnet.Get(name); ok && def.Type == lannet.Plugin {
		// A LAN network is host state: the same file, removed the same way,
		// whichever engine was asked.
		return lannet.Remove(name)
	}
	// -d is what actually deletes the definition: a plain remove, and even
	// remove -f, exit 0 and leave the network in place.
	args := []string{"network", "remove", "-d"}
	if force {
		args = append(args, "-f")
	}
	args = append(args, name)
	out, err := exec.CommandContext(ctx, "appjail", args...).CombinedOutput()
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(string(out))
	if l := strings.ToLower(msg); strings.Contains(l, "in use") || strings.Contains(l, "attached") {
		return fmt.Errorf("%w: %s", engine.ErrInUse, msg)
	}
	return fmt.Errorf("appjail network remove: %w: %s", err, msg)
}

// NetworkParents lists the host bridges, same as any engine would: a LAN
// network hangs off one, and which bridges exist is a fact about the host
// rather than about appjail.
func (b *Backend) NetworkParents(ctx context.Context) ([]engine.NetworkParent, error) {
	return lannet.Parents(ctx)
}

// ParentSetup re-renders the commands that make a bridge. Implements
// engine.NetworkSetupper, so the form behaves the same on either engine.
func (b *Backend) ParentSetup(ctx context.Context, kind, nic, vlan string) (engine.ParentSetup, error) {
	if kind != "lan" {
		return engine.ParentSetup{}, fmt.Errorf("network kind %q takes no parent", kind)
	}
	if vlan != "" && vlan != "auto" {
		n, err := strconv.Atoi(vlan)
		if err != nil || n < 1 || n > 4094 {
			return engine.ParentSetup{}, fmt.Errorf("VLAN id must be a number from 1 to 4094")
		}
	}
	return lannet.ParentSetup(nic, vlan)[0], nil
}

// firstAddr is the first usable address of a network -- what appjail takes as
// the gateway.
func firstAddr(cidr *net.IPNet) string {
	ip := make(net.IP, len(cidr.IP))
	copy(ip, cidr.IP)
	ip = ip.To4()
	if ip == nil {
		return ""
	}
	ip[3]++
	return ip.String()
}

// answers reports whether anything replies at an address. A reply means the
// address is taken, whatever holds it.
func answers(ctx context.Context, ip string) bool {
	if ip == "" {
		return false
	}
	return exec.CommandContext(ctx, "ping", "-c", "2", "-t", "2", ip).Run() == nil
}

// interfaceExists reports whether the host already has an interface by name.
func interfaceExists(ctx context.Context, name string) bool {
	return exec.CommandContext(ctx, "ifconfig", name).Run() == nil
}
