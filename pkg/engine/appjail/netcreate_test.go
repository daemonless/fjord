package appjail

import (
	"strings"
	"testing"
)

// Verbatim from jupiter: tab-separated, the name column padded, and every
// entry suffixed ".appjail". The first row is the network's own gateway.
const reservedHosts = "10.0.0.1\tajnet.appjail\n10.0.0.2\t zensical_zensical.appjail\n"

func TestParseReservedHosts(t *testing.T) {
	got := parseReservedHosts(reservedHosts, "ajnet", "10.0.0.1")
	if len(got) != 1 || got[0] != "zensical_zensical" {
		t.Fatalf("got %q, want [zensical_zensical]", got)
	}
	if strings.Contains(strings.Join(got, ""), ".appjail") {
		t.Errorf("internal suffix leaked into the user list: %q", got)
	}
}

// A network with nothing on it reports no users, not its own gateway.
func TestParseReservedHostsGatewayOnly(t *testing.T) {
	if got := parseReservedHosts("10.0.0.1\tajnet.appjail\n", "ajnet", "10.0.0.1"); len(got) != 0 {
		t.Errorf("got %q, want none", got)
	}
}
