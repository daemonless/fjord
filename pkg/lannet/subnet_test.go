package lannet

import (
	"net"
	"testing"
)

func cidrs(t *testing.T, ss ...string) []*net.IPNet {
	t.Helper()
	var out []*net.IPNet
	for _, s := range ss {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			t.Fatalf("bad fixture %q: %v", s, err)
		}
		out = append(out, n)
	}
	return out
}

func TestFreeSubnetSkipsWhatIsTaken(t *testing.T) {
	got := FreeSubnet(nil)
	if got != "10.100.0.0/24" {
		t.Errorf("first free on an empty host = %q, want 10.100.0.0/24", got)
	}
	// The first two taken: the answer moves on rather than colliding.
	got = FreeSubnet(cidrs(t, "10.100.0.0/24", "10.101.0.0/24"))
	if got != "10.102.0.0/24" {
		t.Errorf("with two taken = %q, want 10.102.0.0/24", got)
	}
	// A wider network swallows the candidates inside it. 10.100.0.0/16 covers
	// 10.100.x, so the next answer has to be outside it -- containment has to
	// be checked both ways, and checking only one misses exactly this.
	got = FreeSubnet(cidrs(t, "10.100.0.0/16"))
	if got != "10.101.0.0/24" {
		t.Errorf("inside a /16 = %q, want 10.101.0.0/24", got)
	}
	// And the host's own LAN is not somewhere to put a private network.
	got = FreeSubnet(cidrs(t, "10.0.0.0/8"))
	if got != "172.20.0.0/24" {
		t.Errorf("with all of 10/8 taken = %q, want 172.20.0.0/24", got)
	}
}

func TestFreeSubnetExhausted(t *testing.T) {
	all := []*net.IPNet{}
	for _, s := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"} {
		all = append(all, cidrs(t, s)...)
	}
	if got := FreeSubnet(all); got != "" {
		t.Errorf("nothing should be free, got %q", got)
	}
}
