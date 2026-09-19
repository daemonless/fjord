// Package lannet owns LAN networks: the ones that give a container or a jail
// its own address on a real segment.
//
// It lives outside both engines because a LAN network is host state, not an
// engine's object. It is a CNI conflist plus a bridge; podman reads the
// conflist through its plugin, appjail reads it only to learn the bridge name
// and then makes its own epair. Keeping creation inside the podman backend
// meant an appjail-only host could not define one at all -- not for any
// technical reason, but because that is where the code happened to sit.
package lannet

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/hostnet"
)

// Plugin is the CNI plugin a LAN network names as its type. podman resolves it
// to a binary; appjail never runs it.
const Plugin = "epair"

// checkSegment6 validates the IPv6 half of a network, when it has one.
func checkSegment6(spec engine.NetworkSpec) (*net.IPNet, error) {
	if spec.Subnet6 == "" {
		return nil, nil
	}
	ip, cidr, err := net.ParseCIDR(spec.Subnet6)
	if err != nil || ip.To4() != nil {
		return nil, fmt.Errorf("invalid IPv6 subnet %q: want a CIDR like fd00:4:103::/64", spec.Subnet6)
	}
	if spec.Gateway6 != "" {
		gw := net.ParseIP(spec.Gateway6)
		if gw == nil || gw.To4() != nil {
			return nil, fmt.Errorf("invalid IPv6 gateway %q", spec.Gateway6)
		}
		if !cidr.Contains(gw) {
			return nil, fmt.Errorf("gateway %s is not inside %s", spec.Gateway6, spec.Subnet6)
		}
	}
	return cidr, nil
}

// withFor records the engine a network was made for, when it was made for
// one. Under a fjord-namespaced key: CNI ignores what it does not recognise,
// and podman still loads a conflist carrying it (verified on 15.1).
func withFor(spec engine.NetworkSpec, doc map[string]any) map[string]any {
	x := map[string]any{}
	if spec.For != "" {
		x["for"] = spec.For
	}
	// The segment this network is on, for a DHCP network where the ipam block
	// carries none. Informational: nothing allocates from it, and podman goes
	// on taking leases. It is what appjail needs to place a pinned address.
	if spec.AddressSource == "dhcp" || spec.AddressSource == "static" {
		if spec.Subnet != "" {
			x["subnet"] = spec.Subnet
		}
		if spec.Gateway != "" {
			x["gateway"] = spec.Gateway
		}
		if spec.Subnet6 != "" {
			x["subnet6"] = spec.Subnet6
		}
		if spec.Gateway6 != "" {
			x["gateway6"] = spec.Gateway6
		}
	}
	if len(x) > 0 {
		doc["x-fjord"] = x
	}
	return doc
}

// conflist renders spec as a CNI network configuration. Split out from the
// write so it can be tested without touching the host.
func Conflist(spec engine.NetworkSpec) ([]byte, error) {
	if !hostnet.NameRe.MatchString(spec.Name) {
		return nil, fmt.Errorf("invalid network name %q: letters, digits, dot, dash and underscore only", spec.Name)
	}
	if spec.Parent == "" {
		return nil, fmt.Errorf("a bridge is required")
	}
	if spec.AddressSource == "dhcp" && spec.Subnet6 != "" {
		return nil, fmt.Errorf("a DHCP network cannot carry an IPv6 segment: its addresses come from the CNI dhcp plugin, which is IPv4-only, and one plugin cannot run two IPAMs -- use a range or static network for IPv6")
	}
	if spec.AddressSource == "dhcp" {
		// Nothing to VALIDATE: the DHCP server supplies address, mask and
		// gateway, and a subnet in the ipam block would only be a second
		// opinion. It is still recorded alongside (see withFor): appjail
		// configures the interface itself when a stack pins an address, and
		// without a prefix length it has nothing to configure it with. Knowing
		// the segment is what lets a fixed address work on a DHCP network.
		plugin := map[string]any{
			"type": Plugin, "master": spec.Parent,
			"ipam":         map[string]any{"type": "dhcp"},
			"capabilities": map[string]bool{"ips": true, "mac": true},
		}
		if spec.MTU > 0 {
			plugin["mtu"] = spec.MTU
		}
		out, err := json.MarshalIndent(withFor(spec, map[string]any{
			"cniVersion": "0.4.0", "name": spec.Name, "plugins": []any{plugin},
		}), "", "  ")
		if err != nil {
			return nil, err
		}
		return append(out, '\n'), nil
	}
	if spec.AddressSource == "static" {
		if _, err := checkSegment6(spec); err != nil {
			return nil, err
		}
		// The CNI static plugin allocates nothing: the address comes from the
		// caller, and a container started without one fails with "IP address
		// not provided by IPAM" rather than quietly getting something. That is
		// the whole point -- this is the network for addresses the operator
		// assigns, and forgetting should be loud.
		//
		// The segment is recorded for the same reason a DHCP network records
		// it: appjail configures the interface itself and needs the prefix.
		if _, _, err := net.ParseCIDR(spec.Subnet); err != nil {
			return nil, fmt.Errorf("invalid subnet %q: want a CIDR like 192.168.4.0/24", spec.Subnet)
		}
		plugin := map[string]any{
			"type": Plugin, "master": spec.Parent,
			"ipam":         map[string]any{"type": "static"},
			"capabilities": map[string]bool{"ips": true, "mac": true},
		}
		if spec.MTU > 0 {
			plugin["mtu"] = spec.MTU
		}
		out, err := json.MarshalIndent(withFor(spec, map[string]any{
			"cniVersion": "0.4.0", "name": spec.Name, "plugins": []any{plugin},
		}), "", "  ")
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
	// host-local takes one range LIST per family, and a route per family with
	// it -- without the ::/0 route a container gets a v6 address and no way
	// off the segment.
	ranges := [][]map[string]any{{rng}}
	routes := []map[string]string{{"dst": "0.0.0.0/0"}}
	cidr6, err6 := checkSegment6(spec)
	if err6 != nil {
		return nil, err6
	}
	if cidr6 != nil {
		rng6 := map[string]any{"subnet": spec.Subnet6}
		if spec.Gateway6 != "" {
			rng6["gateway"] = spec.Gateway6
		}
		ranges = append(ranges, []map[string]any{rng6})
		routes = append(routes, map[string]string{"dst": "::/0"})
	}
	plugin := map[string]any{
		"type":   Plugin,
		"master": spec.Parent,
		"ipam": map[string]any{
			"type":   "host-local",
			"routes": routes,
			"ranges": ranges,
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
	out, err := json.MarshalIndent(withFor(spec, doc), "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
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

// Kind is the LAN network both engines offer. dhcp says whether THIS engine
// can take an address from the segment's own server: podman needs a plugin
// that can do it, appjail runs dhclient inside the jail and always can.
func Kind(dhcp bool) engine.NetworkKind {
	return engine.NetworkKind{
		// The result is a bridge on this host, which both engines can join.
		Shared: true,
		ID:     "lan",
		// Named for the driver it writes, which is what the row it creates
		// reports and what `podman network ls` calls it. "Own IP on a bridge"
		// said bridge for the HOST bridge it attaches to, while the Networks
		// page says bridge for the engine's NAT network -- one word for two
		// opposite things, on two screens the same person reads in a row.
		Label:            "epair",
		Help:             "Containers get their own address on the segment the bridge is on, so they can bind :80/:443 without colliding with the host.",
		ParentLabel:      "Bridge",
		SupportsDHCP:     dhcp,
		ParentSetups:     parentSetups("", ""),
		ParentInterfaces: hostInterfaces(),
		NeedsGateway:     true,
		SupportsMTU:      true,
		SupportsRange:    true,
	}
}

// Create defines a LAN network: it writes the conflist and nothing else. The
// bridge it names is the operator's to make -- persistent host configuration
// fjord will not create behind their back.
func Create(spec engine.NetworkSpec) (engine.Network, error) {
	data, err := Conflist(spec)
	if err != nil {
		return engine.Network{}, err
	}
	path := hostnet.Path(spec.Name)
	if _, err := os.Stat(path); err == nil {
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
	return engine.Network{Name: spec.Name, Driver: Plugin, Subnet: spec.Subnet, Gateway: spec.Gateway}, nil
}

// Remove deletes the definition. The bridge stays: the operator made it, and
// other networks may be on it.
func Remove(name string) error {
	if !hostnet.NameRe.MatchString(name) {
		return fmt.Errorf("invalid network name %q", name)
	}
	path := hostnet.Path(name)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("no network %q at %s", name, path)
	}
	return os.Remove(path)
}

// Parents lists the host bridges a LAN network can hang off, with what the
// host already knows about the segment each is on.
func Parents(ctx context.Context) ([]engine.NetworkParent, error) {
	out, err := exec.CommandContext(ctx, "ifconfig", "-g", "bridge").Output()
	if err != nil {
		return nil, fmt.Errorf("listing bridges: %w", err)
	}
	// In use = already named by a conflist. Read from the definitions rather
	// than from an engine, so the answer does not depend on which engines
	// happen to be installed.
	inUse := map[string]bool{}
	for _, n := range hostnet.List() {
		if n.Bridge != "" {
			inUse[n.Bridge] = true
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

// ParentSetup re-renders the setup commands with the user's choices.
func ParentSetup(nic, vlan string) []engine.ParentSetup { return parentSetups(nic, vlan) }
