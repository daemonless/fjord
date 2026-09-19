package main

import (
	"strings"
	"testing"

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
