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
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/hostnet"
	"github.com/daemonless/fjord/pkg/lannet"
)

// epairPlugin is the CNI type FreeBSD networks use: sysutils/cni-epair, which
// makes an epair per container and puts one end on the parent bridge. FreeBSD
// has no macvlan, and `podman network create` validates --driver against its
// own list (bridge/macvlan/ipvlan) and so cannot express this -- which is why
// the conflist is written directly rather than shelling out to podman.
const epairPlugin = "epair"

// NetworkKinds: what podman can create
// on this host. A private network works anywhere podman does; a LAN network
// needs the epair plugin, so it is FreeBSD-only (on Linux the mechanism is
// kernel macvlan, a different code path).
func (b *Backend) NetworkKinds() []engine.NetworkKind {
	// A private network is just `podman network create`: no plugin, no
	// bridge of the user's, nothing platform-specific. It is offered
	// wherever podman runs, so the two engines describe the same two
	// choices instead of one each.
	kinds := []engine.NetworkKind{{
		ID:                  "nat",
		Label:               "bridge",
		Help:                "The engine creates the bridge and hands out addresses. Containers reach the outside through the host; nothing on your LAN can reach them directly.",
		AddressNote:         "Addresses come from this host: a segment it invents has no DHCP server to ask.",
		SupportsMTU:         true,
		SupportsDescription: false,
	}}
	if k, ok := lanKind(); ok { // Linux: kernel macvlan
		return append([]engine.NetworkKind{k}, kinds...)
	}
	// Under netavark a LAN network needs no plugin: the bridge driver joins
	// the operator's bridge. It cannot take a lease from the segment's DHCP
	// server yet.
	if runtime.GOOS == "freebsd" && b.knownBackend() == backendNetavark {
		k := lannet.Kind(netavarkPlugin())
		if !k.SupportsDHCP {
			k.AddressNote = netavarkDHCPNote
		}
		return append([]engine.NetworkKind{k}, kinds...)
	}
	if !b.lanByPlugin() || !pluginInstalled() {
		return kinds
	}
	// The LAN kind is the host's, not podman's -- podman only knows whether
	// its own plugin can take a DHCP lease.
	return append([]engine.NetworkKind{lannet.Kind(pluginSupports("dhcp"))}, kinds...)
}

// CreateNetwork writes the network's conflist. podman picks it up by reading
// the directory, so there is nothing to restart.
func (b *Backend) CreateNetwork(ctx context.Context, spec engine.NetworkSpec) (engine.Network, error) {
	if len(b.NetworkKinds()) == 0 {
		// networkNote knows every reason -- wrong platform, missing plugin --
		// so it cannot go stale the way a hand-written message did.
		return engine.Network{}, errors.New(b.networkNote())
	}
	if spec.Kind == "nat" {
		return b.createNAT(ctx, spec)
	}
	if spec.Kind != "" && spec.Kind != "lan" {
		return engine.Network{}, fmt.Errorf("unknown network kind %q", spec.Kind)
	}
	if runtime.GOOS == "linux" {
		return b.createMacvlan(ctx, spec)
	}
	if b.networkBackend(ctx) != backendCNI {
		return b.createLANNetavark(ctx, spec, "", "")
	}
	if spec.AddressSource == "static" && !cniPlugin("static") {
		return engine.Network{}, errors.New("the CNI static IPAM plugin is not installed at /usr/local/libexec/cni/static, so a network that assigns no addresses cannot be created here")
	}
	if spec.AddressSource == "dhcp" && !pluginSupports("dhcp") {
		return engine.Network{}, fmt.Errorf("the installed %s plugin cannot do DHCP; upgrade sysutils/cni-epair or choose a pool", epairPlugin)
	}
	existing, err := b.Networks(ctx)
	if err == nil {
		for _, n := range existing {
			if n.Name == spec.Name {
				return engine.Network{}, fmt.Errorf("a network named %q already exists", spec.Name)
			}
		}
	}
	return lannet.Create(ctx, spec)
}

// RemoveNetwork deletes the network's conflist. Unlike a podman-managed
// network, nothing stops the delete on its own: containers already attached
// keep running with an address the plugin can no longer resolve on teardown,
// so attachments are checked here first.
// createNAT makes a private network the runtime owns: its own bridge, its own
// subnet, NAT out through the host. `podman network create` does all of it,
// including picking a free range when none is given -- the same shape appjail
// offers, so the choice on the form is what kind of network it is rather than
// which engine is making it.
func (b *Backend) createNAT(ctx context.Context, spec engine.NetworkSpec) (engine.Network, error) {
	b.networkBackend(ctx) // so the definition is read back from where podman writes it
	if !hostnet.NameRe.MatchString(spec.Name) {
		return engine.Network{}, fmt.Errorf("invalid network name %q: letters, digits, dot, dash and underscore only", spec.Name)
	}
	args := []string{"network", "create"}
	if spec.Subnet != "" {
		ip, _, err := net.ParseCIDR(spec.Subnet)
		if err != nil {
			return engine.Network{}, fmt.Errorf("invalid subnet %q: want a CIDR like 10.100.0.0/24", spec.Subnet)
		}
		// The runtime claims the first address as the gateway. On a segment
		// that already exists that address is the router's.
		if !ip.IsPrivate() {
			return engine.Network{}, fmt.Errorf("%s is not a private range: a network the engine creates must be one it owns (10.x, 172.16-31.x, 192.168.x)", spec.Subnet)
		}
		args = append(args, "--subnet", spec.Subnet)
	}
	if spec.Gateway != "" {
		// Checked here, not just at the UI: podman's own error for a malformed
		// gateway is a bare parse dump, and the subnet right above it is
		// validated -- an unvalidated gateway next to it reads as an oversight.
		if net.ParseIP(spec.Gateway) == nil {
			return engine.Network{}, fmt.Errorf("invalid gateway %q: want an IP address like 10.100.0.1", spec.Gateway)
		}
		args = append(args, "--gateway", spec.Gateway)
	}
	if spec.MTU > 0 {
		args = append(args, "--opt", "mtu="+strconv.Itoa(spec.MTU))
	}
	args = append(args, spec.Name)
	if out, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput(); err != nil {
		return engine.Network{}, fmt.Errorf("podman network create: %s", strings.TrimSpace(string(out)))
	}
	n := engine.Network{Name: spec.Name, Driver: "bridge", Subnet: spec.Subnet, Gateway: spec.Gateway}
	if def, ok := hostnet.Get(spec.Name); ok {
		if n.Subnet == "" {
			n.Subnet, n.Gateway = def.Subnet, def.Gateway
		}
	} else if runtime.GOOS == "freebsd" {
		return engine.Network{}, fmt.Errorf("podman network create %s: no definition was written at %s", spec.Name, hostnet.Path(spec.Name))
	}
	return n, nil
}

// lanNetavarkArgs is the `podman network create` for a LAN network under
// netavark: the bridge driver on a bridge podman does not manage, so the
// container's epair joins the operator's bridge and nothing is NATed. Name
// resolution is off: the gateway is the segment's router, not this host.
func lanNetavarkArgs(spec engine.NetworkSpec) ([]string, error) {
	if !hostnet.NameRe.MatchString(spec.Name) {
		return nil, fmt.Errorf("invalid network name %q: letters, digits, dot, dash and underscore only", spec.Name)
	}
	if !ifaceNameRe.MatchString(spec.Parent) {
		return nil, errors.New("a bridge is required")
	}
	if spec.AddressSource == "dhcp" {
		// The epair plugin as netavark's driver: it asks the segment's DHCP
		// server, as it did under CNI. The segment is only recorded.
		if spec.Subnet6 != "" {
			return nil, errors.New("a DHCP network cannot carry an IPv6 segment: use a range or static network for IPv6")
		}
		args := []string{"network", "create", "-d", epairPlugin, "--interface-name", spec.Parent, "--ipam-driver", "none"}
		if spec.MTU > 0 {
			args = append(args, "--opt", "mtu="+strconv.Itoa(spec.MTU))
		}
		for _, kv := range [][2]string{{"fjord.for", spec.For}, {"fjord.subnet", spec.Subnet}, {"fjord.gateway", spec.Gateway}} {
			if kv[1] != "" {
				args = append(args, "--label", kv[0]+"="+kv[1])
			}
		}
		return append(args, spec.Name), nil
	}
	_, ipnet, err := net.ParseCIDR(spec.Subnet)
	if err != nil {
		return nil, fmt.Errorf("invalid subnet %q: want a CIDR like 192.168.4.0/24", spec.Subnet)
	}
	args := []string{"network", "create", "-d", "bridge", "--opt", "mode=unmanaged",
		"--interface-name", spec.Parent, "--disable-dns", "--subnet", spec.Subnet}
	if spec.Gateway != "" {
		gw := net.ParseIP(spec.Gateway)
		if gw == nil {
			return nil, fmt.Errorf("invalid gateway %q", spec.Gateway)
		}
		if !ipnet.Contains(gw) {
			return nil, fmt.Errorf("gateway %s is not inside %s", spec.Gateway, spec.Subnet)
		}
		args = append(args, "--gateway", spec.Gateway)
	} else if spec.AddressSource != "static" {
		return nil, errors.New("a gateway is required: the segment's router")
	}
	if spec.RangeStart != "" || spec.RangeEnd != "" {
		if spec.RangeStart == "" || spec.RangeEnd == "" {
			return nil, errors.New("give both ends of the range, or neither")
		}
		for _, v := range []string{spec.RangeStart, spec.RangeEnd} {
			if ip := net.ParseIP(v); ip == nil || !ipnet.Contains(ip) {
				return nil, fmt.Errorf("%s is not inside %s", v, spec.Subnet)
			}
		}
		args = append(args, "--ip-range", spec.RangeStart+"-"+spec.RangeEnd)
	}
	if spec.Subnet6 != "" {
		ip, cidr, err := net.ParseCIDR(spec.Subnet6)
		if err != nil || ip.To4() != nil {
			return nil, fmt.Errorf("invalid IPv6 subnet %q: want a CIDR like fd00:4:103::/64", spec.Subnet6)
		}
		args = append(args, "--ipv6", "--subnet", spec.Subnet6)
		if spec.Gateway6 != "" {
			gw := net.ParseIP(spec.Gateway6)
			if gw == nil || gw.To4() != nil || !cidr.Contains(gw) {
				return nil, fmt.Errorf("gateway %s is not inside %s", spec.Gateway6, spec.Subnet6)
			}
			args = append(args, "--gateway", spec.Gateway6)
		}
	}
	if spec.MTU > 0 {
		args = append(args, "--opt", "mtu="+strconv.Itoa(spec.MTU))
	}
	if spec.For != "" {
		args = append(args, "--label", "fjord.for="+spec.For)
	}
	// netavark has no IPAM that hands out nothing and still takes a pinned
	// address, so a static network is a pool fjord never draws from.
	if spec.AddressSource == "static" {
		args = append(args, "--label", "fjord.static=true")
	}
	return append(args, spec.Name), nil
}

// createLANNetavark makes a LAN network on a netavark host.
func (b *Backend) createLANNetavark(ctx context.Context, spec engine.NetworkSpec, vlan, tagged string) (engine.Network, error) {
	args, err := lanNetavarkArgs(spec)
	if err != nil {
		return engine.Network{}, err
	}
	args = withVLAN(args, vlan, tagged)
	if spec.AddressSource == "dhcp" && !netavarkPlugin() {
		return engine.Network{}, errors.New(netavarkDHCPNote)
	}
	if _, ok := hostnet.Get(spec.Name); ok {
		return engine.Network{}, fmt.Errorf("a network named %q already exists", spec.Name)
	}
	if err := exec.CommandContext(ctx, "ifconfig", spec.Parent).Run(); err != nil {
		return engine.Network{}, fmt.Errorf("this host has no bridge %s", spec.Parent)
	}
	if out, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput(); err != nil {
		return engine.Network{}, fmt.Errorf("podman network create: %s", strings.TrimSpace(string(out)))
	}
	return engine.Network{Name: spec.Name, Driver: epairPlugin, Subnet: spec.Subnet, Gateway: spec.Gateway}, nil
}

func (b *Backend) RemoveNetwork(ctx context.Context, name string, force bool) error {
	if !hostnet.NameRe.MatchString(name) {
		return fmt.Errorf("invalid network name %q", name)
	}
	b.networkBackend(ctx)
	def, defined := hostnet.Get(name)
	if !defined {
		// A conflist that does not parse is still a file to remove.
		if _, err := os.Stat(hostnet.ConflistPath(name)); err != nil {
			return fmt.Errorf("no network %q at %s", name, hostnet.Path(name))
		}
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
	// A network the runtime made is the runtime's to remove: it holds state
	// beyond the conflist (the bridge, the IPAM directory). One it never
	// parsed -- a third-party plugin's -- is only the file.
	// One the CNI backend left behind is a file podman no longer knows, and
	// for a bridge network the bridge CNI made for it. That bridge keeps
	// the subnet taken, so the same network could not be made again.
	if defined && def.Legacy {
		if err := lannet.Remove(name); err != nil {
			return err
		}
		if def.Type == "bridge" {
			destroyIdleCNIBridge(ctx, def.Bridge)
		}
		return nil
	}
	if defined && (def.Type != lannet.Plugin || b.knownBackend() != backendCNI) {
		if out, err := exec.CommandContext(ctx, "podman", "network", "rm", "-f", name).CombinedOutput(); err != nil {
			return fmt.Errorf("podman network rm: %s", strings.TrimSpace(string(out)))
		}
		return nil
	}
	return lannet.Remove(name)
}

// RemakeNetwork makes a bridge network the CNI backend left behind again
// under netavark: same name, subnet, gateway and MTU, so the stacks that name
// it start as they are. Implements engine.NetworkRemaker. The old definition
// and the bridge CNI made for it have to go first (the bridge keeps the
// subnet taken); when podman then refuses, the definition is put back.
func (b *Backend) RemakeNetwork(ctx context.Context, name string) (engine.Network, error) {
	if !hostnet.NameRe.MatchString(name) {
		return engine.Network{}, fmt.Errorf("invalid network name %q", name)
	}
	b.networkBackend(ctx)
	def, ok := hostnet.Get(name)
	if !ok || !def.Legacy {
		return engine.Network{}, fmt.Errorf("%s is not a network an earlier network backend left behind", name)
	}
	lan := def.Type == lannet.Plugin
	var vlan, tagged string
	if !lan && def.Type != "bridge" {
		return engine.Network{}, fmt.Errorf("%s is a %s network, which podman cannot make under netavark", name, def.Type)
	}
	spec := engine.NetworkSpec{Name: name, Kind: "nat", Subnet: def.Subnet, Gateway: def.Gateway, MTU: def.MTU}
	if lan {
		spec.Kind, spec.Parent, spec.For = "lan", def.Bridge, def.For
		spec.RangeStart, spec.RangeEnd = def.RangeStart, def.RangeEnd
		spec.Subnet6, spec.Gateway6 = def.Subnet6, def.Gateway6
		switch {
		case def.DHCP:
			spec.AddressSource = "dhcp"
		case def.Static:
			spec.AddressSource = "static"
		}
		// Before the old definition is touched: a refusal leaves it as it was.
		if _, err := lanNetavarkArgs(spec); err != nil {
			return engine.Network{}, err
		}
		// A VLAN on the bridge is part of where the containers are: such a
		// network goes through the epair plugin, which sets it, where
		// podman's own bridge driver cannot.
		if data, err := os.ReadFile(hostnet.ConflistPath(name)); err == nil {
			vlan, tagged = conflistVLAN(data)
		}
		if (vlan != "" || tagged != "") && !netavarkPlugin() {
			return engine.Network{}, fmt.Errorf("%s puts its containers on a VLAN of %s, which under netavark needs the epair plugin at %s", name, def.Bridge, netavarkPluginPath)
		}
	}
	if users, err := b.networkUsersRunning(ctx, name); err == nil && len(users) > 0 {
		return engine.Network{}, fmt.Errorf("%w: %s still runs on the old %s -- stop it first", engine.ErrInUse, strings.Join(users, ", "), name)
	}
	path := hostnet.ConflistPath(name)
	old, err := os.ReadFile(path)
	if err != nil {
		return engine.Network{}, err
	}
	if err := os.Remove(path); err != nil {
		return engine.Network{}, err
	}
	var n engine.Network
	if lan {
		n, err = b.createLANNetavark(ctx, spec, vlan, tagged)
	} else {
		destroyIdleCNIBridge(ctx, def.Bridge)
		n, err = b.createNAT(ctx, spec)
	}
	if err != nil {
		if werr := os.WriteFile(path, old, 0o644); werr != nil {
			return engine.Network{}, fmt.Errorf("%v (and the old definition could not be put back at %s: %v)", err, path, werr)
		}
		return engine.Network{}, err
	}
	keepConflist(name, old)
	return n, nil
}

// KeptConflistDir is where the CNI definitions fjord replaced are kept.
func KeptConflistDir() string { return hostnet.ConflistDir() + ".before-netavark" }

// keepConflist keeps a copy of a CNI definition fjord replaced or removed for
// the move to netavark, outside the directory podman reads, so the move can
// be looked over or undone by hand. Best effort: the network already works.
func keepConflist(name string, data []byte) {
	dir := KeptConflistDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, name+".conflist"), data, 0o644)
}

// networkUsersRunning names the running containers attached to one network.
func (b *Backend) networkUsersRunning(ctx context.Context, name string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://d/v4.0.0/libpod/containers/json", nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var cts []struct {
		Names    []string `json:"Names"`
		Networks []string `json:"Networks"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&cts) != nil {
		return nil, fmt.Errorf("libpod containers/json: %s", resp.Status)
	}
	var out []string
	for _, c := range cts {
		if len(c.Names) > 0 && slices.Contains(c.Networks, name) {
			out = append(out, c.Names[0])
		}
	}
	sort.Strings(out)
	return out, nil
}

// destroyIdleCNIBridge removes a bridge the CNI backend made (cni-podmanN)
// once nothing is on it. Any other bridge is the operator's.
func destroyIdleCNIBridge(ctx context.Context, bridge string) {
	if !strings.HasPrefix(bridge, "cni-podman") {
		return
	}
	out, err := exec.CommandContext(ctx, "ifconfig", bridge).Output()
	if err != nil || strings.Contains(string(out), "member:") {
		return
	}
	_ = exec.CommandContext(ctx, "ifconfig", bridge, "destroy").Run()
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
	if runtime.GOOS == "linux" {
		return linuxParents(), nil
	}
	return lannet.Parents(ctx)
}

// ProbeSegment asks a bridge's wire which segment it is. Implements
// engine.SegmentProber.
func (b *Backend) ProbeSegment(ctx context.Context, parent string) (engine.Segment, error) {
	if runtime.GOOS == "linux" {
		// The probe is FreeBSD's: dhclient on a throwaway epair.
		return engine.Segment{}, engine.ErrNotSupported
	}
	return lannet.ProbeSegment(ctx, parent)
}

// ParentSetup re-renders one setup with the user's choices. Implements
// engine.NetworkSetupper.
func (b *Backend) ParentSetup(ctx context.Context, kind, nic, vlan string) (engine.ParentSetup, error) {
	if kind != "lan" {
		return engine.ParentSetup{}, fmt.Errorf("network kind %q takes no parent", kind)
	}
	if nic != "" && !ifaceNameRe.MatchString(nic) {
		return engine.ParentSetup{}, fmt.Errorf("invalid interface name %q", nic)
	}
	if vlan != "" && vlan != "auto" {
		n, err := strconv.Atoi(vlan)
		if err != nil || n < 1 || n > 4094 {
			return engine.ParentSetup{}, fmt.Errorf("VLAN id must be a number from 1 to 4094")
		}
	}
	return lannet.ParentSetup(nic, vlan)[0], nil
}

// ifaceNameRe bounds a name that lands inside a shell snippet the user pastes
// as root; only what ifconfig would accept gets through.
var ifaceNameRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9._]{0,14}$`)

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
	if runtime.GOOS == "linux" {
		return "" // macvlan and bridge networks: lanKind
	}
	if runtime.GOOS != "freebsd" {
		return "Creating LAN networks is supported on FreeBSD and Linux hosts."
	}
	if b.knownBackend() != backendCNI {
		return ""
	}
	if !pluginInstalled() {
		// Deliberately not just "pkg install cni-epair": the port is not in
		// the tree yet, so that command fails and the reader concludes fjord
		// is wrong rather than early.
		return "Giving containers their own address needs the " + epairPlugin + " CNI plugin, which is not installed. It is not in the ports tree yet -- fetch it to /usr/local/libexec/cni/" + epairPlugin + " from github.com/daemonless/cni-epair and chmod 755 it, or once the port lands, pkg install cni-epair."
	}
	return ""
}

// netavarkDHCPNote is why a LAN network on a FreeBSD netavark host cannot
// take its addresses from the segment's DHCP server.
const netavarkDHCPNote = "Taking addresses from your DHCP server needs the epair plugin for podman's netavark backend, which is not installed at " + netavarkPluginPath + ". Until it is, give the network a range your DHCP server does not hand out, or pin each stack's address."

// netavarkPluginPath is where the epair plugin sits as a netavark driver:
// the same script as the CNI plugin, in one of netavark's plugin directories.
const netavarkPluginPath = "/usr/local/libexec/netavark/epair"

// netavarkPlugin reports whether the epair plugin is installed for netavark.
func netavarkPlugin() bool {
	fi, err := os.Stat(netavarkPluginPath)
	return err == nil && fi.Mode()&0o111 != 0
}

func pluginInstalled() bool { return cniPlugin(epairPlugin) }

// cniPlugin reports whether a CNI plugin binary is present. A conflist naming
// one that is not there loads fine and fails when a container starts on it.
func cniPlugin(name string) bool {
	_, err := os.Stat(filepath.Join("/usr/local/libexec/cni", name))
	return err == nil
}

// conflistVLAN reads the VLAN settings of a conflist's epair plugin: the
// untagged VLAN and the tagged list, "" where not set.
func conflistVLAN(data []byte) (vlan, tagged string) {
	var doc struct {
		Plugins []map[string]json.RawMessage `json:"plugins"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return "", ""
	}
	scalar := func(raw json.RawMessage) string {
		return strings.Trim(strings.TrimSpace(string(raw)), `"`)
	}
	for _, p := range doc.Plugins {
		if raw, ok := p["vlan"]; ok {
			vlan = scalar(raw)
		}
		if raw, ok := p["taggedVlans"]; ok {
			tagged = scalar(raw)
		}
	}
	return vlan, tagged
}

// vlanValueRe bounds what lands on podman's command line as a VLAN option.
var vlanValueRe = regexp.MustCompile(`^[0-9a-z,-]{1,64}$`)

// withVLAN turns a LAN network's create arguments into ones for the epair
// plugin with its VLAN options. Without VLAN settings they are returned as
// they are: podman's own bridge driver needs no plugin.
func withVLAN(args []string, vlan, tagged string) []string {
	if !vlanValueRe.MatchString(vlan) {
		vlan = ""
	}
	if !vlanValueRe.MatchString(tagged) {
		tagged = ""
	}
	if (vlan == "" && tagged == "") || len(args) < 2 {
		return args
	}
	name := args[len(args)-1]
	out := make([]string, 0, len(args)+4)
	for i := 0; i < len(args)-1; i++ {
		switch {
		case args[i] == "-d" && args[i+1] == "bridge":
			out = append(out, "-d", epairPlugin)
			i++
		case args[i] == "--opt" && args[i+1] == "mode=unmanaged":
			i++
		default:
			out = append(out, args[i])
		}
	}
	if vlan != "" {
		out = append(out, "--opt", "vlan="+vlan)
	}
	if tagged != "" {
		out = append(out, "--opt", "taggedVlans="+tagged)
	}
	return append(out, name)
}
