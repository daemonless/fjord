package podman

import (
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/engine"
)

// army, pkg-cache moved onto vlan4, whose VLAN its switch port does not carry.
const armyStart = `time="2026-09-26T13:41:45-04:00" level=warning msg="Failed to load cached network config: network vlan4 not found in CNI cache, falling back to loading network vlan4 from disk"
Error: unable to start container "fecc85de": plugin type="epair" failed (add): cni plugin epair failed: no DHCP response on vlan4bridge for epair0b
`

func TestNoDHCPNamesTheNetwork(t *testing.T) {
	bridge := noDHCPBridge(armyStart)
	if bridge != "vlan4bridge" {
		t.Fatalf("bridge = %q", bridge)
	}
	msg := noDHCPMessage(bridge, []engine.Network{{Name: "lan", Bridge: "lanbridge"}, {Name: "vlan4", Bridge: "vlan4bridge"}})
	for _, want := range []string{"on network vlan4 (bridge vlan4bridge)", "does not reach this host", "Services tab"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
	// A bridge no network claims is still named.
	if msg := noDHCPMessage("br9", nil); !strings.Contains(msg, "on br9,") {
		t.Errorf("unmapped bridge: %s", msg)
	}
	if noDHCPBridge("Error: image not known\n") != "" {
		t.Error("an unrelated failure was read as no-DHCP")
	}
}

func TestLastBytesKeepsTheEnd(t *testing.T) {
	var l lastBytes
	l.Write([]byte(strings.Repeat("x", 20<<10)))
	l.Write([]byte("no DHCP response on vlan4bridge for epair0b"))
	if len(l.String()) != 16<<10 || noDHCPBridge(l.String()) != "vlan4bridge" {
		t.Fatalf("kept %d bytes; tail lost", len(l.String()))
	}
	l.reset()
	if l.String() != "" {
		t.Fatal("reset left data")
	}
}
