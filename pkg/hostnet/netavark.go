package hostnet

import (
	"encoding/json"
	"strconv"
	"strings"
)

// parseNetavark reads a network definition in netavark's format, the JSON
// podman keeps one file per network. fjord's own facts ride
// in its labels, as they ride in a conflist's x-fjord key.
func parseNetavark(name string, data []byte) (Network, bool) {
	var doc struct {
		Name      string `json:"name"`
		Driver    string `json:"driver"`
		Interface string `json:"network_interface"`
		Subnets   []struct {
			Subnet     string `json:"subnet"`
			Gateway    string `json:"gateway"`
			LeaseRange struct {
				Start string `json:"start_ip"`
				End   string `json:"end_ip"`
			} `json:"lease_range"`
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
	n.Compose = composeLabels(doc.Labels)
	n.MTU, _ = strconv.Atoi(doc.Options["mtu"])
	for _, s := range doc.Subnets {
		if strings.Contains(s.Subnet, ":") {
			if n.Subnet6 == "" {
				n.Subnet6, n.Gateway6 = s.Subnet, s.Gateway
			}
		} else if n.Subnet == "" {
			n.Subnet, n.Gateway = s.Subnet, s.Gateway
			n.RangeStart, n.RangeEnd = s.LeaseRange.Start, s.LeaseRange.End
		}
	}
	// A DHCP network declares no subnet; fjord records the segment it found.
	if n.Subnet == "" {
		n.Subnet, n.Gateway = doc.Labels["fjord.subnet"], doc.Labels["fjord.gateway"]
	}
	return n, true
}

// UseConflists points hostnet at dir, read as CNI conflists whatever the OS,
// and returns what puts it back: for tests that write the conflist format,
// which hostnet would otherwise look for as netavark JSON on Linux.
func UseConflists(dir string) (restore func()) {
	mu.Lock()
	old, was := cur, pinned
	cur, pinned = layout{primary: source{dir, ".conflist", parse}}, true
	mu.Unlock()
	return func() {
		mu.Lock()
		cur, pinned = old, was
		mu.Unlock()
	}
}
