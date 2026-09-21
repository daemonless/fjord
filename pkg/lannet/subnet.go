package lannet

import (
	"fmt"
	"net"
)

// FreeSubnet picks a private /24 that overlaps nothing already in use.
//
// A private network needs a range nothing else answers on, and "invent one"
// is a question the host can answer better than the operator can: it knows
// every network already defined and every address on every interface, and the
// operator is being asked to remember them. Deterministic rather than random,
// so clicking twice without creating anything gives the same answer.
//
// Returns "" when every candidate is taken, which the caller reports rather
// than offering a range that collides.
func FreeSubnet(used []*net.IPNet) string {
	for _, c := range candidateSubnets() {
		if !overlapsAny(c, used) {
			return c.String()
		}
	}
	return ""
}

// candidateSubnets walks the private ranges in the order a person would try
// them, staying clear of the low end of each: 10.0.x and 192.168.0-1.x are
// where home routers and other tools put things, and 172.17 is docker's.
func candidateSubnets() []*net.IPNet {
	var out []*net.IPNet
	add := func(s string) {
		if _, n, err := net.ParseCIDR(s); err == nil {
			out = append(out, n)
		}
	}
	for i := 100; i < 200; i++ {
		add(fmt.Sprintf("10.%d.0.0/24", i))
	}
	for i := 20; i <= 31; i++ {
		add(fmt.Sprintf("172.%d.0.0/24", i))
	}
	for i := 100; i < 200; i++ {
		add(fmt.Sprintf("192.168.%d.0/24", i))
	}
	return out
}

// overlapsAny reports whether c shares any address with a network in used.
// Containment either way counts: a /16 already in use swallows the /24 being
// offered, and neither one's base address is inside the other's mask alone.
func overlapsAny(c *net.IPNet, used []*net.IPNet) bool {
	for _, u := range used {
		if u == nil {
			continue
		}
		if c.Contains(u.IP) || u.Contains(c.IP) {
			return true
		}
	}
	return false
}
