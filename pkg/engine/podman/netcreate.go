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
	"sort"
	"strconv"
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
// on this host. A private network works anywhere podman does; a LAN network
// needs the epair plugin, so it is FreeBSD-only (on Linux the mechanism is
// kernel macvlan, a different code path).
func (b *Backend) networkKinds() []engine.NetworkKind {
	// A private network is just `podman network create`: no plugin, no
	// bridge of the user's, nothing platform-specific. It is offered
	// wherever podman runs, so the two engines describe the same two
	// choices instead of one each.
	kinds := []engine.NetworkKind{{
		ID:                  "nat",
		Label:               "Private network",
		Help:                "The engine creates the bridge and hands out addresses. Containers reach the outside through the host; nothing on your LAN can reach them directly.",
		AddressNote:         "Addresses come from this host: a segment it invents has no DHCP server to ask.",
		SupportsMTU:         true,
		SupportsDescription: false,
	}}
	if runtime.GOOS != "freebsd" || !pluginInstalled() {
		return kinds
	}
	return append([]engine.NetworkKind{{
		ID:               "lan",
		Label:            "Own IP on a bridge",
		Help:             "Containers get their own address on the segment the bridge is on, so they can bind :80/:443 without colliding with the host.",
		ParentLabel:      "Bridge",
		SupportsDHCP:     pluginSupports("dhcp"),
		ParentSetups:     parentSetups("", ""),
		ParentInterfaces: hostInterfaces(),
		NeedsGateway:     true,
		SupportsMTU:      true,
		SupportsRange:    true,
	}}, kinds...)
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
	if spec.Kind == "nat" {
		return b.createNAT(ctx, spec)
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
// createNAT makes a private network the runtime owns: its own bridge, its own
// subnet, NAT out through the host. `podman network create` does all of it,
// including picking a free range when none is given -- the same shape appjail
// offers, so the choice on the form is what kind of network it is rather than
// which engine is making it.
func (b *Backend) createNAT(ctx context.Context, spec engine.NetworkSpec) (engine.Network, error) {
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
	if def, ok := hostnet.Get(spec.Name); ok && n.Subnet == "" {
		n.Subnet, n.Gateway = def.Subnet, def.Gateway
	}
	return n, nil
}

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
	// A network the runtime made is the runtime's to remove: it holds state
	// beyond the conflist (the bridge, the IPAM directory). One it never
	// parsed -- a third-party plugin's -- is only the file.
	if def, ok := hostnet.Get(name); ok && def.Type != epairPlugin {
		if out, err := exec.CommandContext(ctx, "podman", "network", "rm", "-f", name).CombinedOutput(); err != nil {
			return fmt.Errorf("podman network rm: %s", strings.TrimSpace(string(out)))
		}
		return nil
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

// parentSetups are the two ways to give containers a LAN address on FreeBSD.
// fjord does not run them: a bridge is persistent host configuration, and the
// untagged one may touch the interface the user is connected through.
// parentSetups renders the one way to make a bridge, with the user's choices
// layered over what the host suggests: which interface it hangs off, and
// whether that is the untagged segment or a tagged VLAN on it.
//
// It is one shape, not two, because the difference between them is a single
// decision -- tagged or not -- and splitting it into two blocks made the
// reader choose before understanding either.
//
// The host cannot know which NIC is cabled to which segment, nor which VLAN
// id the switch tags on that port. It guesses, and the user corrects it.
func parentSetups(nic, vlan string) []engine.ParentSetup {
	up := uplinkNIC()
	unit, br := freeBridge()
	// A bare interface name says nothing about why that one. Carrying the
	// uplink's address lets the note identify it as the interface the reader
	// is connected through.
	uplink := up
	if ip, _, ok := inetOf(context.Background(), up); ok {
		uplink = up + ", which holds this host's address (" + ip.String() + ") and its default route,"
	}

	if nic == "" {
		if nic = spareNIC(up); nic == "" {
			nic = up
		}
	}
	if vlan == "auto" {
		vlan = freeVLANID()
	}

	ps := engine.ParentSetup{
		ID:        "bridge",
		Label:     "Bridge",
		Inputs:    []string{"interface", "vlan"},
		Interface: nic,
		VLAN:      vlan,
	}
	if vlan == "" {
		ps.Snippet, ps.Note = untaggedSnippet(up, nic, unit, br, uplink)
		return []engine.ParentSetup{ps}
	}
	ps.Snippet, ps.Note = vlanSnippet(up, nic, unit, uplink, vlan)
	return []engine.ParentSetup{ps}
}

// vlanSnippet builds the tagged form: a VLAN interface on nic, and a bridge
// holding only that.
func vlanSnippet(up, nic, unit, uplink, id string) (string, string) {
	// A VLAN on an interface nothing else configures needs that interface
	// brought up, or the tag rides a dead wire.
	rcUp, bringUp := "", ""
	if _, _, ok := inetOf(context.Background(), nic); !ok {
		rcUp = "sysrc ifconfig_" + nic + "=\"up\"\n"
		bringUp = "ifconfig " + nic + " up\n"
	}
	snippet := "# vlan " + id + " on " + nic + "\n" +
		"sysrc cloned_interfaces+=\"vlan" + id + " " + unit + "\"\n" +
		rcUp +
		"sysrc ifconfig_vlan" + id + "=\"up vlan " + id + " vlandev " + nic + "\"\n" +
		"sysrc ifconfig_" + unit + "_name=\"vlan" + id + "bridge\"\n" +
		"sysrc ifconfig_vlan" + id + "bridge=\"addm vlan" + id + " up\"\n" +
		"# and now, without rebooting:\n" +
		bringUp +
		"ifconfig vlan" + id + " create vlandev " + nic + " vlan " + id + " up\n" +
		"ifconfig bridge create name vlan" + id + "bridge\n" +
		"ifconfig vlan" + id + "bridge addm vlan" + id + " up"
	return snippet, vlanNote(nic, up, uplink, id)
}

// vlanNote explains the tag on whichever interface it ends up riding: the
// uplink is the safe-over-SSH case worth spelling out, another NIC is not.
func vlanNote(nic, up, uplink, id string) string {
	where := nic
	if nic == up {
		where = uplink + " and a bridge for it. The tag rides the same cable without touching the untagged traffic already on it, so this is safe to run over SSH"
		return "A tagged VLAN on " + where + ". Your switch has to tag VLAN " + id + " on that port."
	}
	return "A tagged VLAN on " + nic + " and a bridge for it. Nothing on " + up + " -- the interface you are connected through -- is touched. Your switch has to tag VLAN " + id + " on the port " + nic + " is plugged into."
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
	return parentSetups(nic, vlan)[0], nil
}

// ifaceNameRe bounds a name that lands inside a shell snippet the user pastes
// as root; only what ifconfig would accept gets through.
var ifaceNameRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9._]{0,14}$`)

// hostInterfaces lists the NICs a parent can be built on, with what the host
// knows about each -- which one is the uplink, which carry no address.
func hostInterfaces() []engine.ParentInterface {
	up := uplinkNIC()
	out, err := exec.Command("ifconfig", "-a").Output()
	if err != nil {
		return nil
	}
	var list []engine.ParentInterface
	cur, ether, addr, grouped, link := "", false, "", false, false
	flush := func() {
		if cur == "" || !ether || grouped {
			return
		}
		detail := "no address"
		if addr != "" {
			detail = addr
		}
		if cur == up {
			detail += ", default route"
		}
		if !link {
			detail += ", no link"
		}
		list = append(list, engine.ParentInterface{Name: cur, Detail: detail, Uplink: cur == up})
	}
	for _, ln := range strings.Split(string(out), "\n") {
		if ln == "" {
			continue
		}
		if !strings.HasPrefix(ln, "\t") && !strings.HasPrefix(ln, " ") {
			flush()
			cur, ether, addr, grouped, link = strings.TrimSuffix(strings.Fields(ln)[0], ":"), false, "", false, false
			continue
		}
		f := strings.Fields(ln)
		switch {
		case f[0] == "ether":
			ether = true
		case f[0] == "inet" && len(f) > 1:
			addr = f[1]
		case f[0] == "groups:":
			grouped = true // a bridge, vlan, epair or tap, never a plain NIC
		case f[0] == "status:" && len(f) > 1 && f[1] == "active":
			link = true
		}
	}
	flush()
	return list
}

// untaggedSnippet builds the bridge on nic itself, with no tag. The default is a spare NIC when the
// host has one -- bridging a second, idle interface cannot interrupt the
// session the user is running fjord over, and bridging the uplink briefly can
// -- but the user can name any interface, so the wording and the commands
// follow what that one actually is rather than how it was chosen.
//
// The rc.conf half clones a numbered bridge and renames it: cloned_interfaces
// hands the name straight to ifconfig, which refuses to create an arbitrary
// one ("SIOCIFCREATE2: Invalid argument"), so a bridge configured under its
// own name comes up fine by hand and is silently missing after a reboot.
func untaggedSnippet(up, nic, unit, br, uplink string) (string, string) {
	if nic == "" {
		nic = up
	}
	addr := ""
	if ip, _, ok := inetOf(context.Background(), nic); ok {
		addr = ip.String()
	}

	lead, note := "", ""
	switch {
	case nic == up:
		lead = "# containers land on the same segment as the host"
		note = "Adds " + uplink + " to a bridge. On FreeBSD it keeps its address, so the host stays reachable, but the bridge takes a moment to learn and this is the interface you are connected through. Containers land on the same segment the host is on."
	case addr == "":
		lead = "# " + nic + " is a second interface with no address -- give it to containers"
		note = nic + " has no address and no traffic on it, so bridging it cannot disturb " + uplink + " the one you are connected through now. " + segmentCaveat(nic)
	default:
		lead = "# give " + nic + " to containers"
		note = nic + " carries " + addr + ". It keeps that address once bridged, but anything already using it sees a brief interruption while the bridge learns. " + uplink + " the one you are connected through, is not touched. " + segmentCaveat(nic)
	}

	// Only bring the interface up here when nothing else configures it;
	// writing ifconfig_<nic>="up" over a DHCP or static line would drop the
	// address at the next boot.
	bringUp := ""
	rcUp := ""
	if addr == "" {
		bringUp = "ifconfig " + nic + " up\n"
		rcUp = "sysrc ifconfig_" + nic + "=\"up\"\n"
	}

	return lead + "\n" +
		"sysrc cloned_interfaces+=\"" + unit + "\"\n" +
		"sysrc ifconfig_" + unit + "_name=\"" + br + "\"\n" +
		rcUp +
		"sysrc ifconfig_" + br + "=\"addm " + nic + " up\"\n" +
		"# and now, without rebooting:\n" +
		bringUp +
		"ifconfig bridge create name " + br + "\n" +
		"ifconfig " + br + " addm " + nic + " up", note
}

// freeBridge picks a bridge the host does not already have: the cloner unit
// rc.conf asks for, and the friendly name it is then renamed to. Without this
// the snippet tells someone to create an interface that exists, ifconfig
// answers "File exists", and the rest of it quietly does nothing.
func freeBridge() (unit, name string) {
	have := map[string]bool{}
	if out, err := exec.Command("ifconfig", "-g", "bridge").Output(); err == nil {
		for _, n := range strings.Fields(string(out)) {
			have[n] = true
		}
	}
	// A unit an earlier setup already claimed is invisible to ifconfig once
	// it has been renamed, so rc.conf has to be consulted as well or the
	// second bridge lands on the first one's unit.
	if out, err := exec.Command("sysrc", "-n", "cloned_interfaces").Output(); err == nil {
		for _, n := range strings.Fields(string(out)) {
			have[n] = true
		}
	}
	unit, name = "bridge0", "lanbridge"
	for i := 0; i < 100; i++ {
		if u := "bridge" + strconv.Itoa(i); !have[u] {
			unit = u
			break
		}
	}
	for i := 1; i < 100; i++ {
		n := "lanbridge"
		if i > 1 {
			n += strconv.Itoa(i)
		}
		if !have[n] {
			name = n
			break
		}
	}
	return unit, name
}

// segmentCaveat says what the host can see about a NIC's cable and what it
// cannot. Carrier is checkable; which segment the other end is on is not, and
// a container taking a DHCP address from the wrong one looks like a fjord bug
// rather than a patch-panel mistake.
func segmentCaveat(nic string) string {
	if !hasLink(nic) {
		return nic + " has no link right now -- nothing is plugged into it, or the port at the other end is down. The bridge will build, but containers on it reach nothing until that is fixed."
	}
	return nic + " has a link, but fjord cannot see which segment it lands on: containers take their addresses from whatever DHCP server answers there, so make sure it is the one you mean."
}

// hasLink reports whether the interface has carrier.
func hasLink(nic string) bool {
	out, err := exec.Command("ifconfig", nic).Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "status: active")
}

// spareNIC reports a second Ethernet interface the host is not using: it has a
// MAC and a link, no address of its own, belongs to no bridge, and is not the
// uplink. Such a NIC can be bridged without disturbing anything. Empty when
// there is none -- the caller then falls back to bridging the uplink.
//
// Selection is by interface group rather than by name: appjail renames epair
// ends to vnet<hex> and jail-side ends to eth0, so a prefix test would let a
// container's wire through, while `ifconfig -a` reports the group either way.
func spareNIC(uplink string) string {
	out, err := exec.Command("ifconfig", "-a").Output()
	if err != nil {
		return ""
	}
	type iface struct{ ether, addressed, grouped, active bool }
	seen := map[string]*iface{}
	members := map[string]bool{}
	order := []string{}
	cur := ""
	for _, ln := range strings.Split(string(out), "\n") {
		if ln == "" {
			continue
		}
		if !strings.HasPrefix(ln, "\t") && !strings.HasPrefix(ln, " ") {
			cur = strings.TrimSuffix(strings.Fields(ln)[0], ":")
			seen[cur] = &iface{}
			order = append(order, cur)
			continue
		}
		e, ok := seen[cur]
		if !ok {
			continue
		}
		f := strings.Fields(ln)
		switch {
		case f[0] == "ether":
			e.ether = true
		case f[0] == "inet":
			e.addressed = true
		case f[0] == "inet6" && len(f) > 1 && !strings.HasPrefix(f[1], "fe80:"):
			e.addressed = true
		case f[0] == "groups:":
			e.grouped = true // lo, bridge, vlan, epair, tap, tun, wg... never a plain NIC
		case f[0] == "status:" && len(f) > 1 && f[1] == "active":
			e.active = true
		case f[0] == "member:" && len(f) > 1:
			members[f[1]] = true
		}
	}
	for _, name := range order {
		e := seen[name]
		if name == uplink || !e.ether || e.addressed || e.grouped || members[name] {
			continue
		}
		// Carrier is the one thing here the host can actually check. A NIC
		// with no link is a socket nothing is plugged into, and suggesting a
		// bridge on it would produce containers that reach nothing at all --
		// worse than the honest interruption of bridging the uplink.
		if e.active {
			return name
		}
	}
	return ""
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
		// Deliberately not just "pkg install cni-epair": the port is not in
		// the tree yet, so that command fails and the reader concludes fjord
		// is wrong rather than early.
		return "Giving containers their own address needs the " + epairPlugin + " CNI plugin, which is not installed. It is not in the ports tree yet -- fetch it to /usr/local/libexec/cni/" + epairPlugin + " from github.com/daemonless/cni-epair and chmod 755 it, or once the port lands, pkg install cni-epair."
	}
	return ""
}

func pluginInstalled() bool {
	_, err := os.Stat(filepath.Join("/usr/local/libexec/cni", epairPlugin))
	return err == nil
}

// freeVLANID picks a VLAN id the host does not already have, so the snippet
// does not tell someone to create what they just created. Falls back to 4 when
// the interface list is unreadable.
func freeVLANID() string {
	have := map[string]bool{}
	if out, err := exec.Command("ifconfig", "-g", "vlan").Output(); err == nil {
		for _, name := range strings.Fields(string(out)) {
			if id, ok := strings.CutPrefix(name, "vlan"); ok {
				have[id] = true
			}
		}
	}
	for i := 4; i < 100; i++ {
		id := strconv.Itoa(i)
		if !have[id] {
			return id
		}
	}
	return "4"
}
