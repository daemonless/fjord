package lannet

import (
	"strconv"
	"testing"
)

func TestParseLease(t *testing.T) {
	seg, err := parseLease("192.168.4.107 255.255.255.0 192.168.4.1\n")
	if err != nil || seg.Subnet != "192.168.4.0/24" || seg.Gateway != "192.168.4.1" {
		t.Fatalf("got %+v, %v", seg, err)
	}
	// No router option: the segment is still known.
	if seg, err := parseLease("10.1.2.3 255.255.0.0"); err != nil || seg.Subnet != "10.1.0.0/16" || seg.Gateway != "" {
		t.Errorf("no router: got %+v, %v", seg, err)
	}
	for _, bad := range []string{"", "192.168.4.107", "nonsense 255.255.255.0", "192.168.4.107 banana"} {
		if _, err := parseLease(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

func TestProbeMACIsLocalUnicast(t *testing.T) {
	for i := 0; i < 50; i++ {
		m := probeMAC()
		b0, err := strconv.ParseUint(m[:2], 16, 8)
		if err != nil {
			t.Fatal(err)
		}
		if b0&0x01 != 0 || b0&0x02 == 0 {
			t.Fatalf("%s is not locally administered unicast", m)
		}
	}
}
