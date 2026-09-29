package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/hostnet"
	"github.com/daemonless/fjord/pkg/stack"
)

// segmentCache remembers what each bridge's wire said and when, so the
// network list can check networks against it without asking. On disk: in
// memory only, a restart forgot lan-range's warning on netlab and syncthing
// went onto it the same afternoon.
type segmentCache struct {
	mu   sync.Mutex
	m    map[string]wireAnswer
	path string // "" = memory only (tests)
}

// wireAnswer is what a bridge's DHCP server said, and when it said it.
type wireAnswer struct {
	engine.Segment
	At time.Time `json:"at"`
}

// wireFresh is how long an answer counts as evidence. fjordd asks again every
// wireEvery; an answer this old means the asking stopped working, and a bridge
// moved to another VLAN must not be judged by where it used to be.
const (
	wireFresh = 72 * time.Hour
	wireEvery = 24 * time.Hour
	wireFirst = 30 * time.Second // after startup: engines and bridges are up
)

// load reads what earlier runs learned. A missing or unreadable file is an
// empty cache: the next scheduled check asks again.
func (c *segmentCache) load(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.path = path
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var m map[string]wireAnswer
	if json.Unmarshal(b, &m) == nil {
		c.m = m
	}
}

// get is the bridge's segment if an answer is on record and fresh.
func (c *segmentCache) get(bridge string) (engine.Segment, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a, ok := c.m[bridge]
	if !ok || time.Since(a.At) > wireFresh {
		return engine.Segment{}, false
	}
	return a.Segment, true
}

func (c *segmentCache) put(bridge string, seg engine.Segment) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]wireAnswer{}
	}
	c.m[bridge] = wireAnswer{Segment: seg, At: time.Now().UTC()}
	if c.path == "" {
		return
	}
	if b, err := json.MarshalIndent(c.m, "", "  "); err == nil {
		if err := stack.WriteFileAtomic(c.path, b, 0o644); err != nil {
			log.Printf("networks: could not save what %s's wire said: %v", bridge, err)
		}
	}
}

// runWireChecks asks, on fjordd's own schedule, each bridge whose segment the
// host cannot see -- shortly after startup, then daily. Not from a request:
// an ask puts an interface on the bridge and takes a DHCP lease, which is not
// something opening a page should do.
func (s *server) runWireChecks() {
	time.Sleep(wireFirst)
	for {
		s.checkWires()
		time.Sleep(wireEvery)
	}
}

func (s *server) checkWires() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	be := s.primaryBackend()
	prober, ok := be.(engine.SegmentProber)
	if !ok {
		return
	}
	// The host's own definitions, not an engine's list: those carry the
	// bridge, which the engines leave for the Networks handler to fill in.
	var nets []engine.Network
	for _, d := range hostnet.List() {
		nets = append(nets, engine.Network{Name: d.Name, Bridge: d.Bridge, Subnet: d.Subnet})
	}
	parents, err := be.NetworkParents(ctx)
	if err != nil {
		return
	}
	for _, bridge := range bridgesToAsk(nets, parents) {
		seg, err := prober.ProbeSegment(ctx, bridge)
		if err != nil {
			log.Printf("networks: could not ask %s's wire: %v", bridge, err)
			continue
		}
		s.segs.put(bridge, seg)
		log.Printf("networks: %s is on %s (its DHCP server says so)", bridge, seg.Subnet)
	}
}

// bridgesToAsk are the LAN bridges (the parents New Network offers) that carry
// a network and hold no host address -- the ones only their wire can
// describe. Not the engines' own bridges behind a private network (podman's
// cni-podmanN): those are no wire to ask. Sorted, each once.
func bridgesToAsk(nets []engine.Network, parents []engine.NetworkParent) []string {
	unknown := map[string]bool{}
	for _, p := range parents {
		if p.Subnet == "" {
			unknown[p.Name] = true
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, n := range nets {
		if !unknown[n.Bridge] || seen[n.Bridge] {
			continue
		}
		seen[n.Bridge] = true
		out = append(out, n.Bridge)
	}
	sort.Strings(out)
	return out
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
