package podman

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/hostnet"
)

// epairPlugin is the CNI type FreeBSD networks use: sysutils/cni-epair, which
// makes an epair per container and puts one end on the parent bridge. FreeBSD
// has no macvlan, and `podman network create` validates --driver against its
// own list (bridge/macvlan/ipvlan) and so cannot express this -- which is why
// the conflist is written directly rather than shelling out to podman.
const epairPlugin = "epair"

// NetworkKinds is podman's contribution to Capabilities: what it can create
// on this host. Only FreeBSD is implemented; on Linux the mechanism is kernel
// macvlan via `podman network create`, which is a different code path.
func (b *Backend) networkKinds() []engine.NetworkKind {
	if runtime.GOOS != "freebsd" || !pluginInstalled() {
		return nil
	}
	return []engine.NetworkKind{{
		ID:            "lan",
		Label:         "Own IP on a bridge",
		Help:          "Containers get their own address on the segment the bridge is on, so they can bind :80/:443 without colliding with the host.",
		ParentLabel:   "Bridge",
		SupportsDHCP:  pluginSupports("dhcp"),
		ParentSetup:   parentSetup(),
		NeedsGateway:  true,
		SupportsMTU:   true,
		SupportsRange: true,
	}}
}

// conflist renders spec as a CNI network configuration. Split out from the
// write so it can be tested without touching the host.
func conflist(spec engine.NetworkSpec) ([]byte, error) {
	if !hostnet.NameRe.MatchString(spec.Name) {
		return nil, fmt.Errorf("invalid network name %q: letters, digits, dot, dash and underscore only", spec.Name)
	}
	if spec.Parent == "" {
		return nil, fmt.Errorf("a bridge is required")
	}
	if spec.AddressSource == "dhcp" {
		// Nothing to validate: the DHCP server supplies address, mask and
		// gateway, and a subnet written here would only be a second opinion.
		plugin := map[string]any{
			"type": epairPlugin, "master": spec.Parent,
			"ipam":         map[string]any{"type": "dhcp"},
			"capabilities": map[string]bool{"ips": true, "mac": true},
		}
		if spec.MTU > 0 {
			plugin["mtu"] = spec.MTU
		}
		out, err := json.MarshalIndent(map[string]any{
			"cniVersion": "0.4.0", "name": spec.Name, "plugins": []any{plugin},
		}, "", "  ")
		if err != nil {
			return nil, err
		}
		return append(out, '\n'), nil
	}
	if _, _, err := net.ParseCIDR(spec.Subnet); err != nil {
		return nil, fmt.Errorf("invalid subnet %q: want a CIDR like 192.168.4.0/24", spec.Subnet)
	}
	_, ipnet, _ := net.ParseCIDR(spec.Subnet)
	gw := net.ParseIP(spec.Gateway)
	if gw == nil {
		return nil, fmt.Errorf("invalid gateway %q", spec.Gateway)
	}
	if !ipnet.Contains(gw) {
		return nil, fmt.Errorf("gateway %s is not inside %s", spec.Gateway, spec.Subnet)
	}
	rng := map[string]any{"subnet": spec.Subnet, "gateway": spec.Gateway}
	for label, v := range map[string]string{"rangeStart": spec.RangeStart, "rangeEnd": spec.RangeEnd} {
		if v == "" {
			continue
		}
		ip := net.ParseIP(v)
		if ip == nil || !ipnet.Contains(ip) {
			return nil, fmt.Errorf("%s %s is not inside %s", label, v, spec.Subnet)
		}
		rng[label] = v
	}
	plugin := map[string]any{
		"type":   epairPlugin,
		"master": spec.Parent,
		"ipam": map[string]any{
			"type":   "host-local",
			"routes": []map[string]string{{"dst": "0.0.0.0/0"}},
			"ranges": [][]map[string]any{{rng}},
		},
		"capabilities": map[string]bool{"ips": true, "mac": true},
	}
	if spec.MTU > 0 {
		plugin["mtu"] = spec.MTU
	}
	doc := map[string]any{
		"cniVersion": "0.4.0",
		"name":       spec.Name,
		"plugins":    []any{plugin},
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// CreateNetwork writes the network's conflist. podman picks it up by reading
// the directory, so there is nothing to restart.
func (b *Backend) CreateNetwork(ctx context.Context, spec engine.NetworkSpec) (engine.Network, error) {
	if len(b.networkKinds()) == 0 {
		// networkNote knows every reason -- wrong platform, missing plugin --
		// so it cannot go stale the way a hand-written message did.
		return engine.Network{}, errors.New(b.networkNote())
	}
	if spec.Kind != "" && spec.Kind != "lan" {
		return engine.Network{}, fmt.Errorf("unknown network kind %q", spec.Kind)
	}
	if spec.AddressSource == "dhcp" && !pluginSupports("dhcp") {
		return engine.Network{}, fmt.Errorf("the installed %s plugin cannot do DHCP; upgrade sysutils/cni-epair or choose a pool", epairPlugin)
	}
	data, err := conflist(spec)
	if err != nil {
		return engine.Network{}, err
	}
	existing, err := b.Networks(ctx)
	if err == nil {
		for _, n := range existing {
			if n.Name == spec.Name {
				return engine.Network{}, fmt.Errorf("a network named %q already exists", spec.Name)
			}
		}
	}
	path := hostnet.Path(spec.Name)
	if _, err := os.Stat(path); err == nil {
		// Reached when the runtime did not list it -- a conflist podman has
		// not picked up, or one it rejected. Still a name that is taken.
		return engine.Network{}, fmt.Errorf("a network named %q is already defined on this host", spec.Name)
	}
	if err := os.MkdirAll(hostnet.ConfDir, 0o755); err != nil {
		return engine.Network{}, err
	}
	// Write-then-rename: podman reads this directory continuously, and a
	// half-written conflist is a broken network rather than an absent one.
	tmp, err := os.CreateTemp(hostnet.ConfDir, "."+spec.Name+".*.tmp")
	if err != nil {
		return engine.Network{}, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return engine.Network{}, err
	}
	if err := tmp.Close(); err != nil {
		return engine.Network{}, err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return engine.Network{}, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return engine.Network{}, err
	}
	return engine.Network{Name: spec.Name, Driver: epairPlugin, Subnet: spec.Subnet, Gateway: spec.Gateway}, nil
}

// RemoveNetwork deletes the network's conflist. Unlike a podman-managed
// network, nothing stops the delete on its own: containers already attached
// keep running with an address the plugin can no longer resolve on teardown,
// so attachments are checked here first.
func (b *Backend) RemoveNetwork(ctx context.Context, name string, force bool) error {
	if !hostnet.NameRe.MatchString(name) {
		return fmt.Errorf("invalid network name %q", name)
	}
	path := hostnet.Path(name)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("no network %q at %s", name, path)
	}
	if !force {
		users, err := b.networkUsers(ctx, name)
		if err != nil {
			return err
		}
		if len(users) > 0 {
			return fmt.Errorf("%w: %s is attached to %s", engine.ErrInUse, name, strings.Join(users, ", "))
		}
	}
	return os.Remove(path)
}

// networkUsers names the containers attached to one network.
func (b *Backend) networkUsers(ctx context.Context, name string) ([]string, error) {
	all, err := b.networkUsersAll(ctx)
	if err != nil {
		return nil, err
	}
	return all[name], nil
}

// networkUsersAll maps network name -> attached container names in a single
// libpod call, so listing N networks does not make N round trips.
func (b *Backend) networkUsersAll(ctx context.Context) (map[string][]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://d/v4.0.0/libpod/containers/json?all=true", nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("libpod containers/json: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("libpod containers/json: unexpected status %s", resp.Status)
	}
	var cts []struct {
		Names    []string `json:"Names"`
		Networks []string `json:"Networks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cts); err != nil {
		return nil, fmt.Errorf("decode containers: %w", err)
	}
	users := map[string][]string{}
	for _, c := range cts {
		if len(c.Names) == 0 {
			continue
		}
		for _, n := range c.Networks {
			users[n] = append(users[n], c.Names[0])
		}
	}
	for n := range users {
		sort.Strings(users[n])
	}
	return users, nil
}

// NetworkParents lists the host bridges a "lan" network can attach to. The
// runtimes' own bridges are excluded: podman's and appjail's are managed by
// those tools, and putting a LAN network on one would cross the two.
func (b *Backend) NetworkParents(ctx context.Context) ([]engine.NetworkParent, error) {
	if len(b.networkKinds()) == 0 {
		return nil, nil
	}
	out, err := exec.CommandContext(ctx, "ifconfig", "-g", "bridge").Output()
	if err != nil {
		return nil, fmt.Errorf("listing bridges: %w", err)
	}
	inUse := map[string]bool{}
	if nets, err := b.Networks(ctx); err == nil {
		for _, n := range nets {
			if d, ok := hostnet.Get(n.Name); ok && d.Bridge != "" {
				inUse[d.Bridge] = true
			}
		}
	}
	var parents []engine.NetworkParent
	for _, line := range strings.Fields(string(out)) {
		if isRuntimeBridge(line) {
			continue
		}
		p := engine.NetworkParent{Name: line, InUse: inUse[line]}
		p.Subnet, p.Gateway, p.HostIP = segmentOf(ctx, line)
		parents = append(parents, p)
	}
	sort.Slice(parents, func(i, j int) bool { return parents[i].Name < parents[j].Name })
	return parents, nil
}

// isRuntimeBridge reports bridges a container runtime manages itself.
func isRuntimeBridge(name string) bool {
	return strings.HasPrefix(name, "cni-") || strings.HasPrefix(name, "podman") || name == "ajnet"
}

// parentSetup is the snippet shown when the host has no bridge to attach to.
// A bridge is persistent host configuration, so fjord does not create one --
// getting it wrong can take the host off the network -- but it can say exactly
// what to write.
//
// The VLAN form is shown because it is the safe one: a new VLAN interface and
// a new bridge containing only it touch nothing that already carries traffic.
// Bridging an existing NIC is the variant that strands a remote host, so it is
// flagged rather than spelled out.
func parentSetup() string {
	nic := uplinkNIC()
	// Lines are kept short: this renders in a modal, and anything much over
	// ~55 columns is clipped rather than wrapped.
	return "# vlan 4 is an example; " + nic + " is this host's uplink\n" +
		"sysrc cloned_interfaces+=\"vlan4 vlan4bridge\"\n" +
		"sysrc ifconfig_vlan4=\"up vlan 4 vlandev " + nic + "\"\n" +
		"sysrc ifconfig_vlan4bridge=\"addm vlan4 up\"\n" +
		"# and now, without rebooting:\n" +
		"ifconfig vlan4 create vlandev " + nic + " vlan 4 up\n" +
		"ifconfig bridge create name vlan4bridge\n" +
		"ifconfig vlan4bridge addm vlan4 up"
}

// uplinkNIC guesses the interface a new VLAN should hang off: the one carrying
// the default route, or -- when that is already a bridge, as it is on a host
// whose LAN is bridged for containers -- that bridge's physical member.
// Falls back to a placeholder rather than a wrong name.
func uplinkNIC() string {
	out, err := exec.Command("netstat", "-rn", "-f", "inet").Output()
	if err != nil {
		return "<nic>"
	}
	iface := ""
	for _, ln := range strings.Split(string(out), "\n") {
		if f := strings.Fields(ln); len(f) > 3 && f[0] == "default" {
			iface = f[len(f)-1]
			break
		}
	}
	if iface == "" {
		return "<nic>"
	}
	memb, err := exec.Command("ifconfig", iface).Output()
	if err != nil {
		return iface
	}
	for _, ln := range strings.Split(string(memb), "\n") {
		f := strings.Fields(ln)
		if len(f) >= 2 && f[0] == "member:" && !isVirtualWire(f[1]) {
			return f[1] // the bridge's uplink, not the bridge
		}
	}
	return iface
}

// isVirtualWire reports a container's half of an epair. appjail renames its
// ends to "vnet<hex>", so the prefix check has to cover both or a bridge whose
// first member happens to be a container would be suggested as a VLAN parent.
func isVirtualWire(name string) bool {
	return strings.HasPrefix(name, "epair") || strings.HasPrefix(name, "vnet")
}

// segmentOf reports what the host already knows about the segment a bridge is
// on: the subnet, a likely gateway, and the host's own address there.
//
// The address may be on the bridge itself (an untagged LAN bridge usually
// carries the host's IP) or on a member (a VLAN bridge usually does not, but
// the VLAN interface in it does). All three are empty when nothing carries an
// address -- a bridge built only for containers -- and the form then asks.
func segmentOf(ctx context.Context, bridge string) (subnet, gateway, hostIP string) {
	ifaces := []string{bridge}
	if out, err := exec.CommandContext(ctx, "ifconfig", bridge).Output(); err == nil {
		for _, ln := range strings.Split(string(out), "\n") {
			f := strings.Fields(ln)
			if len(f) >= 2 && f[0] == "member:" && !isVirtualWire(f[1]) {
				ifaces = append(ifaces, f[1])
			}
		}
	}
	for _, name := range ifaces {
		ip, mask, ok := inetOf(ctx, name)
		if !ok {
			continue
		}
		n := ip.Mask(mask)
		ones, _ := mask.Size()
		subnet = fmt.Sprintf("%s/%d", n.String(), ones)
		hostIP = ip.String()
		gateway = gatewayFor(ctx, n, mask)
		return subnet, gateway, hostIP
	}
	return "", "", ""
}

// inetOf reads an interface's first IPv4 address and mask. FreeBSD prints the
// mask in hex ("netmask 0xffffff00").
func inetOf(ctx context.Context, iface string) (net.IP, net.IPMask, bool) {
	out, err := exec.CommandContext(ctx, "ifconfig", iface).Output()
	if err != nil {
		return nil, nil, false
	}
	for _, ln := range strings.Split(string(out), "\n") {
		f := strings.Fields(ln)
		if len(f) < 4 || f[0] != "inet" {
			continue
		}
		ip := net.ParseIP(f[1]).To4()
		if ip == nil || ip.IsLoopback() {
			continue
		}
		var m uint32
		if _, err := fmt.Sscanf(f[3], "0x%x", &m); err != nil {
			continue
		}
		return ip, net.IPv4Mask(byte(m>>24), byte(m>>16), byte(m>>8), byte(m)), true
	}
	return nil, nil, false
}

// gatewayFor prefers the host's real default gateway when it sits on this
// segment; otherwise it offers the conventional first address, which is a
// guess the user can correct.
func gatewayFor(ctx context.Context, network net.IP, mask net.IPMask) string {
	if out, err := exec.CommandContext(ctx, "netstat", "-rn", "-f", "inet").Output(); err == nil {
		for _, ln := range strings.Split(string(out), "\n") {
			f := strings.Fields(ln)
			if len(f) < 2 || f[0] != "default" {
				continue
			}
			if gw := net.ParseIP(f[1]).To4(); gw != nil && gw.Mask(mask).Equal(network) {
				return gw.String()
			}
		}
	}
	first := make(net.IP, len(network))
	copy(first, network)
	first[len(first)-1]++
	return first.String()
}

// pluginSupports asks the installed plugin whether it can do something beyond
// the CNI contract. A plugin that predates the FEATURES command simply fails,
// which is the right answer: it cannot.
func pluginSupports(feature string) bool {
	cmd := exec.Command(filepath.Join("/usr/local/libexec/cni", epairPlugin))
	cmd.Env = append(os.Environ(), "CNI_COMMAND=FEATURES")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	for _, ln := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(ln) == feature {
			return true
		}
	}
	return false
}

// networkNote explains an empty kind list. A missing plugin is the common one
// and is fixable, so it says how: writing a conflist for a plugin that is not
// installed produces a network that accepts containers and then fails every
// one of them at start.
func (b *Backend) networkNote() string {
	if runtime.GOOS != "freebsd" {
		return "Creating networks is only supported on FreeBSD hosts; on Linux use podman network create."
	}
	if !pluginInstalled() {
		return "The " + epairPlugin + " CNI plugin is not installed, so a network created here could not run. Install it with: pkg install cni-epair"
	}
	return ""
}

func pluginInstalled() bool {
	_, err := os.Stat(filepath.Join("/usr/local/libexec/cni", epairPlugin))
	return err == nil
}
