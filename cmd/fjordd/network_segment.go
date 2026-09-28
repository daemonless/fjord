package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/daemonless/fjord/pkg/engine"
)

// segmentCache remembers what each bridge's wire said when probed, so the
// network list can check networks against it without asking again.
type segmentCache struct {
	mu sync.Mutex
	m  map[string]engine.Segment
}

func (c *segmentCache) get(bridge string) (engine.Segment, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	seg, ok := c.m[bridge]
	return seg, ok
}

func (c *segmentCache) put(bridge string, seg engine.Segment) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]engine.Segment{}
	}
	c.m[bridge] = seg
}

// handleNetworkProbe asks a bridge's wire which segment it is on:
// POST /api/networks/probe {"parent": "lanbridge"}.
//
// Always 200 with found true/false: nothing answering is an answer the form
// shows ("type the subnet; fjord could not check it"), not an error.
func (s *server) handleNetworkProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req struct {
		Parent string `json:"parent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Parent == "" {
		http.Error(w, "parent required", 400)
		return
	}
	reply := map[string]any{"found": false}
	prober, ok := s.backendForRequest(r).(engine.SegmentProber)
	if !ok {
		reply["reason"] = "this engine cannot ask the network"
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		seg, err := prober.ProbeSegment(ctx, req.Parent)
		if err != nil {
			reply["reason"] = err.Error()
		} else {
			s.segs.put(req.Parent, seg)
			reply = map[string]any{"found": true, "subnet": seg.Subnet, "gateway": seg.Gateway}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(reply)
}

// wireOf is what fjord knows about the segment a bridge is on, and from where:
// the host's own address on it first, then a DHCP server's answer.
func (s *server) wireOf(bridge string, parents []engine.NetworkParent) (subnet, from string) {
	for _, p := range parents {
		if p.Name == bridge && p.Subnet != "" {
			return p.Subnet, "this host's address on it"
		}
	}
	if seg, ok := s.segs.get(bridge); ok {
		return seg.Subnet, "its DHCP server"
	}
	return "", ""
}

// offWire reports whether subnet is not on the wire's segment: its base
// address falls outside it. Unparseable input is not this check's to judge.
func offWire(subnet, wire string) bool {
	_, sub, err1 := net.ParseCIDR(subnet)
	_, w, err2 := net.ParseCIDR(wire)
	if err1 != nil || err2 != nil {
		return false
	}
	return !w.Contains(sub.IP)
}

// wireMismatch is the reason a network about to be created on bridge does not
// fit what is known about its wire, or "".
func (s *server) wireMismatch(bridge, subnet string, parents []engine.NetworkParent) string {
	if bridge == "" || subnet == "" {
		return ""
	}
	wire, from := s.wireOf(bridge, parents)
	if wire == "" || !offWire(subnet, wire) {
		return ""
	}
	return fmt.Sprintf("%s is on %s (%s says so) -- %s is not on that wire, so nothing there could answer its containers", bridge, wire, from, subnet)
}

// markWireWarnings flags networks whose subnet does not fit their bridge's
// wire. With the wire unknown, networks on one bridge that disagree with each
// other are flagged together: one of them is wrong, and fjord cannot say which.
// This is how lan-static claimed 192.168.4.0/24 next to two 192.168.86.0/24
// networks on army's lanbridge, and nothing said so.
func (s *server) markWireWarnings(nets []engine.Network, parents []engine.NetworkParent) {
	byBridge := map[string][]int{}
	for i, n := range nets {
		if n.Bridge != "" && n.Subnet != "" {
			byBridge[n.Bridge] = append(byBridge[n.Bridge], i)
		}
	}
	for bridge, idx := range byBridge {
		if wire, from := s.wireOf(bridge, parents); wire != "" {
			for _, i := range idx {
				if offWire(nets[i].Subnet, wire) {
					nets[i].WireWarning = fmt.Sprintf("%s is on %s (%s says so), not %s", bridge, wire, from, nets[i].Subnet)
				}
			}
			continue
		}
		for _, i := range idx {
			var others []string
			for _, j := range idx {
				if j != i && offWire(nets[i].Subnet, nets[j].Subnet) && offWire(nets[j].Subnet, nets[i].Subnet) {
					others = append(others, fmt.Sprintf("%s says %s", nets[j].Name, nets[j].Subnet))
				}
			}
			if len(others) > 0 {
				sort.Strings(others)
				nets[i].WireWarning = fmt.Sprintf("%s is on the same bridge (%s) but %s -- they cannot all be right", nets[i].Name, bridge, joinAnd(others))
			}
		}
	}
}

func joinAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	out := items[0]
	for _, it := range items[1 : len(items)-1] {
		out += ", " + it
	}
	return out + " and " + items[len(items)-1]
}
