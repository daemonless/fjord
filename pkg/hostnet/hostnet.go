// Package hostnet reads the host's LAN network definitions -- the bridge,
// subnet and gateway that make up a network a container or jail can be given
// an address on.
//
// On FreeBSD these live in CNI conflists, because that is what podman reads.
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
	"time"
)

// ConfDir is where podman reads network definitions from. Every file here is
// a live network.
var ConfDir = "/usr/local/etc/cni/net.d"

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
	MTU     int
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
}

// Path returns the conflist path for a network name.
func Path(name string) string { return filepath.Join(ConfDir, name+".conflist") }

// Get reads one network's definition. ok is false when the name is invalid or
// no such network is defined.
func Get(name string) (Network, bool) {
	if !NameRe.MatchString(name) {
		return Network{}, false
	}
	data, err := os.ReadFile(Path(name))
	if err != nil {
		return Network{}, false
	}
	return parse(name, data)
}

// List returns every defined network, ordered by name.
func List() []Network {
	entries, err := os.ReadDir(ConfDir)
	if err != nil {
		return nil
	}
	var out []Network
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".conflist")
		if !ok || !NameRe.MatchString(name) {
			continue
		}
		if n, ok := Get(name); ok {
			out = append(out, n)
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
					Subnet  string `json:"subnet"`
					Gateway string `json:"gateway"`
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
	out, err := exec.CommandContext(ctx, "jexec", strings.TrimSpace(string(jidOut)), "ifconfig").Output()
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
	out, err := exec.CommandContext(ctx, "jexec", strings.TrimSpace(string(jidOut)), "ifconfig").Output()
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
