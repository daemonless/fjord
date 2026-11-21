// Package hostnet reads the host's LAN network definitions -- the bridge,
// subnet and gateway that make up a network a container or jail can be given
// an address on.
//
// They live where podman's network backend reads them: CNI conflists or
// netavark JSON.
// AppJail has no equivalent registry: a jail is attached to a bridge by name
// and told its address directly. Keeping the definitions in one place is what
// lets "vlan5" mean the same network to both engines instead of one meaning a
// conflist and the other meaning a bridge.
package hostnet

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// cniDir is where podman's CNI backend reads conflists, netavarkDir where its
// netavark backend keeps a network's JSON. An empty one is a format this
// host's podman never uses.
var (
	cniDir      = "/usr/local/etc/cni/net.d"
	netavarkDir = "/usr/local/etc/containers/networks"
)

// source is one directory of network definitions and how to read them.
type source struct {
	dir, ext string
	parse    func(name string, data []byte) (Network, bool)
}

// layout is where definitions are read from: primary is the format podman's
// backend loads and writes, legacy what an earlier backend left behind.
// Legacy definitions still name a real bridge, so they are listed, not lost.
type layout struct {
	primary source
	legacy  []source
}

var (
	mu  sync.RWMutex
	cur = layout{primary: source{cniDir, ".conflist", parse}}
	// pinned: a test chose the layout, and podman's answer must not move it.
	pinned bool
)

func current() layout {
	mu.RLock()
	defer mu.RUnlock()
	return cur
}

// SetBackend follows podman's network backend ("cni" or "netavark"): the
// engine calls it with what podman reports. A host that moved to netavark
// keeps its conflists readable.
func SetBackend(backend string) {
	if cniDir == "" || netavarkDir == "" {
		return
	}
	cni := source{cniDir, ".conflist", parse}
	next := layout{primary: cni}
	if backend == "netavark" {
		next = layout{primary: source{netavarkDir, ".json", parseNetavark}, legacy: []source{cni}}
	}
	mu.Lock()
	if !pinned {
		cur = next
	}
	mu.Unlock()
}

// Dir is where podman reads network definitions from. Every file here is a
// live network.
func Dir() string { return current().primary.dir }

// ConflistDir is where a CNI conflist goes, whatever podman's backend: the
// place AppJail's LAN networks are kept too.
func ConflistDir() string {
	l := current()
	if l.primary.ext == ".conflist" || cniDir == "" {
		return l.primary.dir
	}
	return cniDir
}

// ConflistPath is the conflist path for a network name.
func ConflistPath(name string) string {
	l := current()
	if l.primary.ext == ".conflist" || cniDir == "" {
		return filepath.Join(l.primary.dir, name+l.primary.ext)
	}
	return filepath.Join(cniDir, name+".conflist")
}

// NameRe bounds a network name: it becomes a filename and an interface name
// component, so neither path separators nor exotic characters are allowed.
var NameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)

// Network is one LAN network definition.
type Network struct {
	Name string
	// Type is the CNI plugin the network runs on ("epair", "bridge", ...).
	Type    string
	Bridge  string // the host bridge it hangs off
	Subnet  string
	Gateway string
	// RangeStart/RangeEnd: the part of Subnet a pool hands out ("" = all of
	// it), IPv4.
	RangeStart, RangeEnd string
	MTU                  int
	// DHCP: addresses come from the segment's own server, so there is no
	// subnet here to read. Without this a DHCP network looks like a broken one.
	DHCP bool
	// Subnet6/Gateway6: the IPv6 half of the segment, when the network has
	// one. Read from the second ipam range for a pool network, or from the
	// fjord key for a static one, exactly as the v4 half is.
	Subnet6  string
	Gateway6 string
	// Static: nothing allocates on this network -- every stack brings its own
	// address. Distinct from DHCP (the segment's server allocates) and from a
	// range (the runtime's IPAM does).
	Static bool
	// For names the engine this network was made for, or "" for any of them.
	//
	// The network itself is shared -- one bridge, one conflist, one segment,
	// which is the whole point of defining it on the host rather than per
	// engine. What is not shared is what each engine can DO with it: podman
	// reads the whole conflist, while appjail reads only the bridge name and
	// cannot ask host-local for an address. Recording the intended engine lets
	// the form offer only what that engine can honour, without splitting one
	// segment into two networks.
	For string
	// Legacy: defined in a format podman's current backend does not load (a
	// conflist on a netavark host). The bridge is real; podman cannot use it.
	Legacy bool
	// Compose: the network compose makes for one project and removes with
	// it, not one somebody made to attach stacks to.
	Compose bool
}

// Path returns the definition path for a network name in the format podman's
// backend loads.
func Path(name string) string {
	p := current().primary
	return filepath.Join(p.dir, name+p.ext)
}

// Get reads one network's definition. ok is false when the name is invalid or
// no such network is defined.
func Get(name string) (Network, bool) {
	if !NameRe.MatchString(name) {
		return Network{}, false
	}
	l := current()
	if n, ok := l.primary.get(name); ok {
		return n, true
	}
	for _, src := range l.legacy {
		if n, ok := src.get(name); ok {
			n.Legacy = true
			return n, true
		}
	}
	return Network{}, false
}

func (s source) get(name string) (Network, bool) {
	data, err := os.ReadFile(filepath.Join(s.dir, name+s.ext))
	if err != nil {
		return Network{}, false
	}
	return s.parse(name, data)
}

func (s source) names() []string {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), s.ext); ok && NameRe.MatchString(name) {
			out = append(out, name)
		}
	}
	return out
}

// List returns every defined network, ordered by name. A name defined in both
// formats is the current backend's.
func List() []Network {
	l := current()
	var out []Network
	seen := map[string]bool{}
	for _, name := range l.primary.names() {
		if n, ok := l.primary.get(name); ok {
			out = append(out, n)
			seen[name] = true
		}
	}
	for _, src := range l.legacy {
		for _, name := range src.names() {
			if seen[name] {
				continue
			}
			if n, ok := src.get(name); ok {
				n.Legacy = true
				out = append(out, n)
				seen[name] = true
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Parse reads one network definition from conflist bytes. Exported so the
// write side (lannet.Conflist) can be round-tripped in tests without touching
// the host's config directory.
func Parse(name string, data []byte) (Network, bool) { return parse(name, data) }

func parse(name string, data []byte) (Network, bool) {
	var doc struct {
		Args struct {
			Labels map[string]string `json:"podman_labels"`
		} `json:"args"`
		Fjord struct {
			For      string `json:"for"`
			Subnet   string `json:"subnet"`
			Gateway  string `json:"gateway"`
			Subnet6  string `json:"subnet6"`
			Gateway6 string `json:"gateway6"`
		} `json:"x-fjord"`
		Plugins []struct {
			Type   string `json:"type"`
			Master string `json:"master"`
			Bridge string `json:"bridge"` // the plugin accepts either spelling
			MTU    int    `json:"mtu"`
			IPAM   struct {
				Type   string `json:"type"`
				Ranges [][]struct {
					Subnet     string `json:"subnet"`
					Gateway    string `json:"gateway"`
					RangeStart string `json:"rangeStart"`
					RangeEnd   string `json:"rangeEnd"`
				} `json:"ranges"`
			} `json:"ipam"`
		} `json:"plugins"`
	}
	if json.Unmarshal(data, &doc) != nil || len(doc.Plugins) == 0 {
		return Network{}, false
	}
	p := doc.Plugins[0]
	n := Network{Name: name, Type: p.Type, Bridge: p.Master, MTU: p.MTU, DHCP: p.IPAM.Type == "dhcp", Static: p.IPAM.Type == "static", For: doc.Fjord.For}
	if n.Bridge == "" {
		n.Bridge = p.Bridge
	}
	n.Compose = composeLabels(doc.Args.Labels)
	// host-local takes one range list per family, in the order fjord wrote
	// them. Classify by what the address looks like rather than by position,
	// so a conflist written by hand with only a v6 range still reads right.
	for _, r := range p.IPAM.Ranges {
		if len(r) == 0 || r[0].Subnet == "" {
			continue
		}
		if strings.Contains(r[0].Subnet, ":") {
			if n.Subnet6 == "" {
				n.Subnet6, n.Gateway6 = r[0].Subnet, r[0].Gateway
			}
			continue
		}
		if n.Subnet == "" {
			n.Subnet, n.Gateway = r[0].Subnet, r[0].Gateway
			n.RangeStart, n.RangeEnd = r[0].RangeStart, r[0].RangeEnd
		}
	}
	// A DHCP network has no ipam ranges; the segment is recorded beside them.
	// DHCP stays true -- this says what the wire is, not who allocates on it.
	if n.Subnet == "" {
		n.Subnet, n.Gateway = doc.Fjord.Subnet, doc.Fjord.Gateway
	}
	if n.Subnet6 == "" {
		n.Subnet6, n.Gateway6 = doc.Fjord.Subnet6, doc.Fjord.Gateway6
	}
	return n, true
}

// composeLabels says a network's labels are a compose project's.
func composeLabels(labels map[string]string) bool {
	return labels["com.docker.compose.project"] != "" || labels["io.podman.compose.project"] != ""
}

// AddressOf returns the address CNI's IPAM assigned a container on a network,
// or "" if it has none there.
//
// This is the only reliable source on FreeBSD: podman does not parse a
// third-party plugin's conflist, so `inspect` reports no address at all for an
// epair network, and the compose only records one when the user pinned it.
// host-local writes <confdir>/<network>/<address> containing the container id,
// which is exactly the mapping needed.
func AddressOf(network, containerID string) string {
	if !NameRe.MatchString(network) || containerID == "" {
		return ""
	}
	dir := filepath.Join(ipamStateDir, network)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		addr := e.Name()
		// Allocation files are named for the address they hold.
		if net.ParseIP(addr) == nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, addr))
		if err != nil {
			continue
		}
		// "<container id>\n<ifname>"
		id, _, _ := strings.Cut(strings.TrimSpace(string(data)), "\n")
		if strings.TrimSpace(id) == containerID {
			return addr
		}
	}
	return ""
}

// ipamStateDir is where host-local records its allocations.
var ipamStateDir = "/var/run/cni/networks"

// cniResultsDir is libcni's cache of what each plugin returned, one file per
// container, network and interface. Unlike ipamStateDir it is under /var/lib
// and survives a reboot.
var cniResultsDir = "/var/lib/cni/results"

// ResultAddressOf is the IPv4 address a network gave a container, as the
// plugin reported it to libcni, or "". It is the one source that says which
// network an address came from: the jail's own list does not, and two
// networks on one segment (a pool and a DHCP network on the same bridge)
// made guessing by subnet hand the DHCP lease to neither.
func ResultAddressOf(network, containerID string) string {
	if containerID == "" {
		return ""
	}
	files, _ := filepath.Glob(filepath.Join(cniResultsDir, network+"-"+containerID+"-*"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var c struct {
			Result struct {
				IPs []struct {
					Address string `json:"address"`
				} `json:"ips"`
			} `json:"result"`
		}
		if json.Unmarshal(b, &c) != nil {
			continue
		}
		for _, ip := range c.Result.IPs {
			a := ip.Address
			if i := strings.IndexByte(a, '/'); i >= 0 {
				a = a[:i]
			}
			if parsed := net.ParseIP(a); parsed != nil && parsed.To4() != nil {
				return a
			}
		}
	}
	return ""
}

// MACOf is the MAC a container holds on a network, as the plugin reported
// it, or "". podman's own inspect leaves MacAddress empty on FreeBSD, and on
// a DHCP network the MAC is what the lease -- and so the address -- follows.
func MACOf(network, containerID string) string {
	if containerID == "" {
		return ""
	}
	files, _ := filepath.Glob(filepath.Join(cniResultsDir, network+"-"+containerID+"-*"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var c struct {
			IfName string `json:"ifName"`
			Result struct {
				Interfaces []struct {
					Name string `json:"name"`
					MAC  string `json:"mac"`
				} `json:"interfaces"`
			} `json:"result"`
		}
		if json.Unmarshal(b, &c) != nil {
			continue
		}
		// The container's side, not the host's: epair reports both.
		for _, i := range c.Result.Interfaces {
			if i.Name == c.IfName && i.MAC != "" {
				return i.MAC
			}
		}
	}
	return ""
}

// ReleaseAddress frees one reservation if the container holding it is gone,
// with no age guard: for an address a stack pins, which nothing else should
// hold. Under a cni-epair that never releases on DEL, a recreate leaves the
// pinned address reserved by the container it just removed, and the new one
// is refused it ("duplicate allocation") until this frees it.
func ReleaseAddress(network, addr string, live func(id string) bool) bool {
	if !NameRe.MatchString(network) || net.ParseIP(addr) == nil {
		return false
	}
	p := filepath.Join(ipamStateDir, network, addr)
	data, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	id, _, _ := strings.Cut(strings.TrimSpace(string(data)), "\n")
	if id = strings.TrimSpace(id); id == "" || live(id) {
		return false
	}
	return os.Remove(p) == nil
}

// ReleaseOrphans removes host-local reservations on network whose container
// no longer exists (live says whether an ID does), returning the addresses it
// freed.
//
// A reservation outlives its container two ways: cni-epair before 10ab333
// never released on DEL, so every recreate leaked one (saturn's tautulli moved
// .200 -> .201 on an update and .200 stayed held by a gone container), and an
// unclean shutdown skips DEL entirely. Either way the address is lost to the
// pool, and a container whose own orphan it is can never start.
//
// Files younger than minAge are left alone: a container another stack is
// creating right now can have its reservation before the caller's list of
// live containers includes it.
func ReleaseOrphans(network string, live func(id string) bool, minAge time.Duration) []string {
	if !NameRe.MatchString(network) {
		return nil
	}
	dir := filepath.Join(ipamStateDir, network)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var freed []string
	for _, e := range entries {
		addr := e.Name()
		if net.ParseIP(addr) == nil {
			continue // lock, last_reserved_ip.N
		}
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < minAge {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, addr))
		if err != nil {
			continue
		}
		// "<container id>\r\n<ifname>"
		id, _, _ := strings.Cut(strings.TrimSpace(string(data)), "\n")
		id = strings.TrimSpace(id)
		if id == "" || live(id) {
			continue
		}
		if os.Remove(filepath.Join(dir, addr)) == nil {
			freed = append(freed, addr)
		}
	}
	return freed
}

// JailAddress reports the address a jail holds, read from inside it.
//
// Nothing outside the jail reliably knows it. `appjail jail list` fills
// NETWORK_IP4 only for its own virtualnets, and podman on FreeBSD does not
// parse a third-party plugin's conflist at all -- so for a jail on a host
// bridge, whichever engine started it, this is the only source. A container
// whose address came from DHCP has none recorded on the host either, which is
// why AddressOf alone is not enough.
//
// Both podman and appjail name the jail after the thing they started: a
// container ID, or the jail name.
// JailAddresses is every non-loopback address a jail holds.
//
// JailAddress returns the first, which is the wrong one as soon as a jail is
// on two networks: a container on the LAN and on a private segment reported
// whichever interface ifconfig listed first, so the link offered to open the
// app pointed at an address no browser can reach.
func JailAddresses(ctx context.Context, jail string) []string {
	jidOut, err := exec.CommandContext(ctx, "jls", "-j", jail, "jid").Output()
	if err != nil {
		return nil
	}
	out, err := exec.CommandContext(ctx, "ifconfig", "-j", strings.TrimSpace(string(jidOut))).Output()
	if err != nil {
		return nil
	}
	var addrs []string
	for _, ln := range strings.Split(string(out), "\n") {
		f := strings.Fields(ln)
		if len(f) < 2 || f[0] != "inet" {
			continue
		}
		if ip := net.ParseIP(f[1]); ip != nil && !ip.IsLoopback() {
			addrs = append(addrs, ip.String())
		}
	}
	return addrs
}

// InSubnet reports whether addr falls inside the network's own segment. It is
// how an address is matched to the interface it came from when the runtime
// reports a list and says nothing about which is which.
func InSubnet(addr, subnet string) bool {
	ip := net.ParseIP(addr)
	_, cidr, err := net.ParseCIDR(subnet)
	if ip == nil || err != nil {
		return false
	}
	return cidr.Contains(ip)
}

func JailAddress(ctx context.Context, jail string) string {
	jidOut, err := exec.CommandContext(ctx, "jls", "-j", jail, "jid").Output()
	if err != nil {
		return ""
	}
	out, err := exec.CommandContext(ctx, "ifconfig", "-j", strings.TrimSpace(string(jidOut))).Output()
	if err != nil {
		return ""
	}
	for _, ln := range strings.Split(string(out), "\n") {
		f := strings.Fields(ln)
		if len(f) < 2 || f[0] != "inet" {
			continue
		}
		if ip := net.ParseIP(f[1]); ip != nil && !ip.IsLoopback() {
			return ip.String()
		}
	}
	return ""
}

// NoAddressReason explains, in the terms the user chose the network in, why a
// container attached to it holds no address. The network's own definition
// says which cause is possible, so the message names it instead of leaving
// the reader to guess what "no address" means.
func NoAddressReason(network string) string {
	def, ok := Get(network)
	switch {
	case !ok:
		return "no address: " + network + " is attached but not defined on this host"
	case def.DHCP:
		return "no address: " + network + " takes addresses from a DHCP server and none answered on " + def.Bridge
	default:
		return "no address: " + network + " should have assigned one, so attaching it failed"
	}
}

// ReleaseFailedAdds frees reservations a failed network setup left behind on
// the very container that will retry: a start that failed in the bridge
// plugin's add keeps host-local's reservation but caches no result, so every
// later start runs add again and host-local refuses the address it already
// gave that same container ("duplicate allocation"). Nothing undoes it --
// teardown needs the cached result -- and the stack cannot start again until
// the file is removed by hand (immich_database_1 on netlab, 2026-09-21).
//
// Freed only when the reservation names a container stopped (one of the
// caller's, not running) and libcni has no result for it on this network: a
// container that really holds the address always has its result cached.
func ReleaseFailedAdds(network string, stopped func(id string) bool) []string {
	if !NameRe.MatchString(network) {
		return nil
	}
	dir := filepath.Join(ipamStateDir, network)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var freed []string
	for _, e := range entries {
		addr := e.Name()
		if net.ParseIP(addr) == nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, addr))
		if err != nil {
			continue
		}
		id, _, _ := strings.Cut(strings.TrimSpace(string(data)), "\n")
		if id = strings.TrimSpace(id); id == "" || !stopped(id) {
			continue
		}
		if cached, _ := filepath.Glob(filepath.Join(cniResultsDir, network+"-"+id+"-*")); len(cached) > 0 {
			continue // the add completed: the address is really its
		}
		if os.Remove(filepath.Join(dir, addr)) == nil {
			freed = append(freed, addr)
		}
	}
	return freed
}

// MatchAddresses assigns a jail's addresses to the networks it is on, given
// the ones already known (out, changed in place). A jail lists its addresses
// and not which interface each came from: first each network takes the
// address in its own subnet (subnetOf, "" when it records none), then a
// network with no subnet (DHCP: the lease carries it) takes what is left.
func MatchAddresses(addrs, networks []string, out map[string]string, subnetOf func(string) string) {
	claimed := map[string]bool{}
	for _, a := range out {
		claimed[a] = true
	}
	for _, n := range networks {
		if _, known := out[n]; known {
			continue
		}
		sub := subnetOf(n)
		if sub == "" {
			continue
		}
		for _, a := range addrs {
			if !claimed[a] && InSubnet(a, sub) {
				out[n], claimed[a] = a, true
				break
			}
		}
	}
	inOther := func(a, self string) bool {
		for _, n := range networks {
			if n != self {
				if sub := subnetOf(n); sub != "" && InSubnet(a, sub) {
					return true
				}
			}
		}
		return false
	}
	for _, n := range networks {
		if _, known := out[n]; known {
			continue
		}
		for _, a := range addrs {
			if claimed[a] || inOther(a, n) {
				continue
			}
			out[n], claimed[a] = a, true
			break
		}
	}
}

// DefinedSubnet is the subnet a network fjord defines records, "" when none.
func DefinedSubnet(network string) string {
	if def, ok := Get(network); ok {
		return def.Subnet
	}
	return ""
}
