package main

import (
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/hostnet"
)

// "1" was accepted and written into `ifconfig sb_x:1/24`, which FreeBSD reads
// as 0.0.0.1: a jail that looks configured and reaches nothing.
func TestAddressUnusable(t *testing.T) {
	lan := hostnet.Network{Subnet: "192.168.4.0/24", Gateway: "192.168.4.1"}
	for _, tc := range []struct{ addr, want string }{
		{"192.168.4.33", ""},
		{"1", "not an IPv4 address"},
		{"", "not an IPv4 address"},
		{"banana", "not an IPv4 address"},
		{"10.9.9.9", "is not in 192.168.4.0/24"},
		{"192.168.4.0", "is the network address"},
		{"192.168.4.255", "is the broadcast address"},
		{"192.168.4.1", "is the gateway"},
	} {
		got := addressUnusable(tc.addr, lan)
		if tc.want == "" && got != "" {
			t.Errorf("%q should be fine, got %q", tc.addr, got)
		}
		if tc.want != "" && !strings.Contains(got, tc.want) {
			t.Errorf("%q: got %q, want it to mention %q", tc.addr, got, tc.want)
		}
	}
	// With no segment recorded there is nothing to check against, but the
	// address itself still has to be one.
	none := hostnet.Network{}
	if got := addressUnusable("10.9.9.9", none); got != "" {
		t.Errorf("unknown segment should accept any valid address, got %q", got)
	}
	if got := addressUnusable("1", none); got == "" {
		t.Error("an invalid address is invalid regardless of the segment")
	}
}

// A network the ENGINE allocates on has no conflist, so hostnet knows nothing
// about it -- the engine's own report is what the address gets checked against.
// Its gateway is deliberately not carried over: appjail hands out ajnet's
// 10.0.0.1 as the first address in the range.
func TestAttachmentsUnusableEngineNetwork(t *testing.T) {
	ajnet := []engine.Network{{Name: "ajnet", Subnet: "10.0.0.0/10", Gateway: "10.0.0.1"}}
	for _, tc := range []struct{ ip, want string }{
		{"192.168.86.1", "is not in 10.0.0.0/10"},
		{"10.0.0.0", "is the network address"},
		{"10.63.255.255", "is the broadcast address"},
		{"10.0.0.1", ""}, // appjail's MINADDR: allocatable despite being the gateway
		{"10.5.5.5", ""},
	} {
		got := attachmentsUnusable([]compose.Attachment{{Network: "ajnet", IP: tc.ip}}, ajnet)
		if tc.want == "" && got != "" {
			t.Errorf("%s should be fine on ajnet, got %q", tc.ip, got)
		}
		if tc.want != "" && !strings.Contains(got, tc.want) {
			t.Errorf("%s: got %q, want it to mention %q", tc.ip, got, tc.want)
		}
	}
	// Without the engine's list there is nothing to check against, as before.
	if got := attachmentsUnusable([]compose.Attachment{{Network: "ajnet", IP: "192.168.86.1"}}, nil); got != "" {
		t.Errorf("unknown network should not be judged, got %q", got)
	}
}
