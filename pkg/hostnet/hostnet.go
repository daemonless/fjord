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
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
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

func parse(name string, data []byte) (Network, bool) {
	var doc struct {
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
	n := Network{Name: name, Type: p.Type, Bridge: p.Master, MTU: p.MTU, DHCP: p.IPAM.Type == "dhcp"}
	if n.Bridge == "" {
		n.Bridge = p.Bridge
	}
	if r := p.IPAM.Ranges; len(r) > 0 && len(r[0]) > 0 {
		n.Subnet, n.Gateway = r[0][0].Subnet, r[0][0].Gateway
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
