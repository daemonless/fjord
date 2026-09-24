package compose

import (
	"strings"
	"testing"
)

func TestPinAddress(t *testing.T) {
	// tautulli's shape: a plain list entry, with a comment to keep.
	list := "services:\n  tautulli:\n    image: t\n    # the web ui\n    networks:\n      - lan\n  other:\n    image: o\n    networks:\n      - lan\n"
	out, err := PinAddress(list, "tautulli", "lan", "192.168.86.201")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "ipv4_address: 192.168.86.201") || !strings.Contains(out, "# the web ui") {
		t.Errorf("list form:\n%s", out)
	}
	if strings.Count(out, "ipv4_address") != 1 {
		t.Errorf("pinned more than tautulli:\n%s", out)
	}

	// Map form with a MAC already there: keep it, add the address.
	mapped := "services:\n  app:\n    image: a\n    networks:\n      lan:\n        mac_address: 02:00:00:00:00:01\n"
	out, _ = PinAddress(mapped, "app", "lan", "192.168.86.9")
	if !strings.Contains(out, "mac_address: 02:00:00:00:00:01") || !strings.Contains(out, "ipv4_address: 192.168.86.9") {
		t.Errorf("map form:\n%s", out)
	}

	// An address already pinned is the operator's: left as is.
	pinned := "services:\n  app:\n    image: a\n    networks:\n      lan:\n        ipv4_address: 192.168.86.50\n"
	if out, _ = PinAddress(pinned, "app", "lan", "192.168.86.9"); out != pinned {
		t.Errorf("changed an existing pin:\n%s", out)
	}

	for _, bad := range []struct{ c, svc, net string }{
		{"services:\n  app:\n    image: a\n", "app", "lan"}, // on no named network
		{list, "nope", "lan"},                               // no such service
		{list, "tautulli", "vlan5"},                         // not on that network
		{"version: '3'\n", "app", "lan"},                    // no services at all
	} {
		if _, err := PinAddress(bad.c, bad.svc, bad.net, "10.0.0.1"); err == nil {
			t.Errorf("PinAddress(%q, %s, %s) accepted", bad.c, bad.svc, bad.net)
		}
	}
}

// A DHCP network's lease follows the MAC, so that is what gets pinned; one
// the operator set is theirs.
func TestPinMAC(t *testing.T) {
	in := "services:\n  app:\n    image: x\n    networks:\n      - lan-dhcp\n"
	out, err := PinMAC(in, "app", "lan-dhcp", "58:9c:fc:10:cd:c0")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "mac_address: 58:9c:fc:10:cd:c0") {
		t.Errorf("not pinned:\n%s", out)
	}
	again, _ := PinMAC(out, "app", "lan-dhcp", "02:00:00:00:00:01")
	if again != out {
		t.Errorf("an existing MAC was replaced:\n%s", again)
	}
}
