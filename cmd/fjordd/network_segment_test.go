package main

import (
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/engine"
)

func TestOffWire(t *testing.T) {
	cases := []struct {
		sub, wire string
		off       bool
	}{
		{"192.168.4.0/24", "192.168.4.0/24", false},
		{"192.168.4.128/25", "192.168.4.0/24", false},
		{"192.168.86.0/24", "192.168.4.0/24", true},
		{"garbage", "192.168.4.0/24", false},
	}
	for _, c := range cases {
		if got := offWire(c.sub, c.wire); got != c.off {
			t.Errorf("offWire(%s, %s) = %v, want %v", c.sub, c.wire, got, c.off)
		}
	}
}

// army: lanbridge carries the host's 192.168.86.14, and lan-static said
// 192.168.4.0/24.
func TestWireWarningFromHostAddress(t *testing.T) {
	s := &server{}
	parents := []engine.NetworkParent{{Name: "lanbridge", Subnet: "192.168.86.0/24", HostIP: "192.168.86.14"}}
	nets := []engine.Network{
		{Name: "lan-range", Bridge: "lanbridge", Subnet: "192.168.86.0/24"},
		{Name: "lan-static", Bridge: "lanbridge", Subnet: "192.168.4.0/24"},
	}
	s.markWireWarnings(nets, parents)
	if nets[0].WireWarning != "" {
		t.Errorf("lan-range flagged: %s", nets[0].WireWarning)
	}
	if !strings.Contains(nets[1].WireWarning, "192.168.86.0/24") {
		t.Errorf("lan-static not flagged: %q", nets[1].WireWarning)
	}
	if msg := s.wireMismatch("lanbridge", "192.168.4.0/24", parents); msg == "" {
		t.Error("create on the wrong segment was not refused")
	}
}

// netlab: lanbridge has no host address; once its DHCP server has answered,
// lan-range claiming 192.168.86.0/24 there is caught.
func TestWireWarningFromProbe(t *testing.T) {
	s := &server{}
	nets := []engine.Network{{Name: "lan-range", Bridge: "lanbridge", Subnet: "192.168.86.0/24"}}
	s.markWireWarnings(nets, nil)
	if nets[0].WireWarning != "" {
		t.Fatalf("flagged with nothing known: %s", nets[0].WireWarning)
	}
	s.segs.put("lanbridge", engine.Segment{Subnet: "192.168.4.0/24", Gateway: "192.168.4.1"})
	s.markWireWarnings(nets, nil)
	if !strings.Contains(nets[0].WireWarning, "DHCP") {
		t.Errorf("want the DHCP answer named, got %q", nets[0].WireWarning)
	}
	if s.wireMismatch("lanbridge", "192.168.4.0/24", nil) != "" {
		t.Error("the right segment was refused")
	}
}

// Nothing known about the wire, two networks on it disagree: both flagged.
func TestWireWarningSiblings(t *testing.T) {
	s := &server{}
	nets := []engine.Network{
		{Name: "a", Bridge: "br", Subnet: "10.0.1.0/24"},
		{Name: "b", Bridge: "br", Subnet: "10.0.2.0/24"},
		{Name: "c", Bridge: "other", Subnet: "10.0.9.0/24"},
	}
	s.markWireWarnings(nets, nil)
	if !strings.Contains(nets[0].WireWarning, "b says 10.0.2.0/24") || !strings.Contains(nets[1].WireWarning, "a says 10.0.1.0/24") {
		t.Errorf("siblings not flagged: %q / %q", nets[0].WireWarning, nets[1].WireWarning)
	}
	if nets[2].WireWarning != "" {
		t.Errorf("lone network flagged: %s", nets[2].WireWarning)
	}
}
