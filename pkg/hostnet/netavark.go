package hostnet

import (
	"encoding/json"
	"strconv"
	"strings"
)

// parseNetavark reads a network definition in netavark's format -- what
// podman keeps in /etc/containers/networks on Linux. fjord's own facts ride
// in its labels, as they ride in a conflist's x-fjord key.
func parseNetavark(name string, data []byte) (Network, bool) {
	var doc struct {
		Name      string `json:"name"`
		Driver    string `json:"driver"`
		Interface string `json:"network_interface"`
		Subnets   []struct {
			Subnet  string `json:"subnet"`
			Gateway string `json:"gateway"`
		} `json:"subnets"`
		Options map[string]string `json:"options"`
		Labels  map[string]string `json:"labels"`
		IPAM    struct {
			Driver string `json:"driver"`
		} `json:"ipam_options"`
	}
	if json.Unmarshal(data, &doc) != nil || doc.Driver == "" {
		return Network{}, false
	}
	n := Network{
		Name:   name,
		Type:   doc.Driver,
		Bridge: doc.Interface,
		DHCP:   doc.IPAM.Driver == "dhcp",
		Static: doc.IPAM.Driver == "none",
		For:    doc.Labels["fjord.for"],
	}
	n.MTU, _ = strconv.Atoi(doc.Options["mtu"])
	for _, s := range doc.Subnets {
		if strings.Contains(s.Subnet, ":") {
			if n.Subnet6 == "" {
				n.Subnet6, n.Gateway6 = s.Subnet, s.Gateway
			}
		} else if n.Subnet == "" {
			n.Subnet, n.Gateway = s.Subnet, s.Gateway
		}
	}
	// A DHCP network declares no subnet; fjord records the segment it found.
	if n.Subnet == "" {
		n.Subnet, n.Gateway = doc.Labels["fjord.subnet"], doc.Labels["fjord.gateway"]
	}
	return n, true
}
