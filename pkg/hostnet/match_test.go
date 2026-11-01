package hostnet

import "testing"

// A jail on lan (DHCP: no subnet recorded) and a private network: the private
// one takes its own address by subnet, lan the one left over. With only one
// address and no network for it, the stack page had no link (crafting-apps).
func TestMatchAddresses(t *testing.T) {
	subnets := map[string]string{"crafting-a_priv": "10.100.0.0/24"}
	out := map[string]string{}
	MatchAddresses([]string{"10.100.0.3", "192.168.4.137"}, []string{"lan", "crafting-a_priv"}, out,
		func(n string) string { return subnets[n] })
	if out["lan"] != "192.168.4.137" || out["crafting-a_priv"] != "10.100.0.3" {
		t.Fatalf("got %v", out)
	}
	// Order of the jail's list does not matter.
	out = map[string]string{}
	MatchAddresses([]string{"192.168.4.137", "10.100.0.3"}, []string{"lan", "crafting-a_priv"}, out,
		func(n string) string { return subnets[n] })
	if out["lan"] != "192.168.4.137" {
		t.Fatalf("reversed: got %v", out)
	}
}
